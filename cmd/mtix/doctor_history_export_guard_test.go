// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// seedGuardRegistration records a session registration of the given build
// version, seen just now, straight into the board database.
func seedGuardRegistration(t *testing.T, version string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(app.mtixDir, "data", "mtix.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	seen := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(`INSERT INTO agent_registrations(agent_id,harness,uid,host_id,origin,registered_at,last_seen,registrar_version,attach_protocol,returned_seq_mark)
		VALUES('old-worker','local','u','h','manual',?,?,?,1,0)`, seen, seen, version)
	require.NoError(t, err)
}

type guardRow struct {
	ID, Status, Detail, Fix string
	Data                    map[string]any
}

func guardDoctorRow(t *testing.T, jsonOut bool) (guardRow, string) {
	t.Helper()
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	var out bytes.Buffer
	_ = runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: true}) // a failing row makes doctor exit non-zero; the rows are still written
	var rows []guardRow
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), out.String())
	for _, r := range rows {
		if r.ID == "history.export.guard" {
			if !jsonOut {
				var text bytes.Buffer
				require.NoError(t, runDoctor(t.Context(), &text, svc, doctorOutputOptions{}))
				return r, text.String()
			}
			return r, out.String()
		}
	}
	return guardRow{}, ""
}

// With the switch off and nothing detected the row is omitted, so a board that
// never uses the feature sees an unchanged report (as the history.size row).
func TestDoctor_HistoryExportGuard_SwitchOffNoOlderBuild_RowOmitted(t *testing.T) {
	initTestApp(t)
	row, _ := guardDoctorRow(t, true)
	assert.Empty(t, row.ID)
}

func TestDoctor_HistoryExportGuard_SwitchOnNoOlderBuild_PassesAndStatesBlindSpot(t *testing.T) {
	initTestApp(t)
	_, err := app.configSvc.Set("history.export", "true")
	require.NoError(t, err)
	row, _ := guardDoctorRow(t, true)
	assert.Equal(t, "pass", row.Status)
	assert.Contains(t, row.Detail, "cannot see", "the blind spot is in the detail")
	assert.Equal(t, service.FirstHistoryVersion, row.Data["min_version"])
	assert.Equal(t, true, row.Data["switch_on"])
	assert.Empty(t, row.Data["blockers"])
}

func TestDoctor_HistoryExportGuard_SwitchOffOlderBuild_WarnsWithFixInTextAndJSON(t *testing.T) {
	initTestApp(t)
	seedGuardRegistration(t, "0.5.4-beta")
	row, jsonOut := guardDoctorRow(t, true)
	assert.Equal(t, "warn", row.Status)
	assert.Contains(t, row.Detail, service.FirstHistoryVersion)
	assert.Contains(t, row.Fix, "upgrade mtix to "+service.FirstHistoryVersion)
	blockers, ok := row.Data["blockers"].([]any)
	require.True(t, ok, jsonOut)
	require.Len(t, blockers, 1)
	assert.Equal(t, "old-worker", blockers[0].(map[string]any)["name"])
	_, text := guardDoctorRow(t, false)
	assert.Contains(t, text, "history.export.guard")
	assert.Contains(t, text, "old-worker")
}

func TestDoctor_HistoryExportGuard_SwitchOnOlderBuild_FailsAndSaysHowToTurnOff(t *testing.T) {
	initTestApp(t)
	_, err := app.configSvc.Set("history.export", "true")
	require.NoError(t, err)
	seedGuardRegistration(t, "0.5.0")
	row, _ := guardDoctorRow(t, true)
	assert.Equal(t, "fail", row.Status)
	assert.Contains(t, row.Fix, "mtix config set history.export false")
}

// A build that holds history never blocks itself, whatever its version string:
// main builds stamp git-describe versions that read as 0.5.4.
func TestDoctor_HistoryExportGuard_ThisBuildsOwnDescribeStampedSignals_DoNotBlock(t *testing.T) {
	initTestApp(t)
	require.NoError(t, app.store.RecordDispatcherHeartbeat(t.Context(), sqlite.DispatcherHeartbeat{
		HostID: "h", UID: "u", PID: 7, Protocol: 1, Version: "v0.5.4-beta-312-g7ed998a8-dirty",
		SeenAt: time.Now().UTC().Format(time.RFC3339)}))
	require.NoError(t, runConfigSetQuiet(t, "history.export", "true"))
	row, _ := guardDoctorRow(t, true)
	assert.Equal(t, "pass", row.Status)
}

func runConfigSetQuiet(t *testing.T, key, value string) error {
	t.Helper()
	var err error
	captureStdout(t, func() { err = runConfigSet(key, value) })
	return err
}

// The switch travels through the tracked config.yaml: a clone that pulled
// history.export: true never ran the guard in Set, so its doctor row must fail.
func TestDoctor_HistoryExportGuard_SwitchEnabledByConfigFileEdit_FailsInThisClone(t *testing.T) {
	initTestApp(t)
	seedGuardRegistration(t, "0.5.4-beta")
	cfg := filepath.Join(app.mtixDir, "config.yaml")
	raw, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, append(raw, []byte("history:\n  export: true\n")...), 0o644))
	reloaded, err := service.NewConfigService(cfg)
	require.NoError(t, err)
	app.configSvc = reloaded
	require.True(t, app.configSvc.HistoryExport())
	row, _ := guardDoctorRow(t, true)
	assert.Equal(t, "fail", row.Status)
	assert.Contains(t, row.Fix, "mtix config set history.export false")
}

func TestHistoryExportGuardRow_CheckCannotRun_FailsWhenSwitchOnWarnsWhenOff(t *testing.T) {
	errCheck := errors.New("database locked")
	assert.Equal(t, service.DoctorFail, historyExportGuardRow(service.HistoryGuardReport{}, true, errCheck).Status)
	assert.Equal(t, service.DoctorWarn, historyExportGuardRow(service.HistoryGuardReport{}, false, errCheck).Status)
	assert.Contains(t, historyExportGuardRow(service.HistoryGuardReport{}, true, errCheck).Detail, "database locked")
}

func TestConfigSet_HistoryExportOn_CLIPath_RefusedWithFixThenAllowedAfterUpgrade(t *testing.T) {
	initTestApp(t)
	seedGuardRegistration(t, "0.5.4-beta")
	err := runConfigSet("history.export", "true")
	require.Error(t, err)
	assert.Contains(t, err.Error(), service.FirstHistoryVersion)
	assert.Contains(t, err.Error(), "old-worker")
	assert.False(t, app.configSvc.HistoryExport())

	seedGuardUpgrade(t)
	captureStdout(t, func() { require.NoError(t, runConfigSet("history.export", "true")) })
	assert.True(t, app.configSvc.HistoryExport())
}

func seedGuardUpgrade(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(app.mtixDir, "data", "mtix.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	_, err = db.Exec(`UPDATE agent_registrations SET capabilities = ?`, sqlite.HistoryReaderCapability)
	require.NoError(t, err)
}

type fakeHistoryGuard struct {
	rep   service.HistoryGuardReport
	err   error
	calls int
}

func (f *fakeHistoryGuard) Evaluate(context.Context) (service.HistoryGuardReport, error) {
	f.calls++
	return f.rep, f.err
}

// The doctor row and config set reach the guard only through the service the
// app holds: a fake in its place decides both.
func TestHistoryExportGuard_DoctorRowAndConfigSet_GoThroughTheGuardService(t *testing.T) {
	initTestApp(t)
	fake := &fakeHistoryGuard{rep: service.HistoryGuardReport{MinVersion: service.FirstHistoryVersion,
		Blockers: []service.HistoryGuardBlocker{{Source: "daemon", Name: "fake:1", Version: "0.5.4-beta"}}}}
	app.historyGuard = fake
	app.configSvc.SetHistoryExportGuard(func() (service.HistoryGuardReport, error) {
		return app.historyGuard.Evaluate(context.Background())
	})
	row, _ := guardDoctorRow(t, true)
	assert.Equal(t, "warn", row.Status)
	assert.Contains(t, row.Detail, "fake:1")
	callsAfterDoctor := fake.calls
	require.GreaterOrEqual(t, callsAfterDoctor, 1)
	require.Error(t, runConfigSet("history.export", "true"))
	assert.Greater(t, fake.calls, callsAfterDoctor, "config set asks the same service")
}
