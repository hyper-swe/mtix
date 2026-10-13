// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Sticky hub-connected mark: evidence, doctor check and sync-command wiring
// (MTIX-112.17.3, ADR-007 §3.5, A9).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findCheck returns the named check of a doctor report, failing when absent.
func findCheck(t *testing.T, report DoctorReport, name string) DoctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	require.Failf(t, "check missing", "no %s check in the report", name)
	return DoctorCheck{}
}

// findFailingCheck returns the single failing check of a doctor report.
func findFailingCheck(t *testing.T, report DoctorReport) DoctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if !check.Pass {
			return check
		}
	}
	require.Fail(t, "no failing check in the report")
	return DoctorCheck{}
}

func hubMarkCheck(t *testing.T) DoctorCheck {
	t.Helper()
	return findCheck(t, appendBoardIdentityChecks(DoctorReport{OverallPass: true}), "board.hub-mark")
}

func TestAppendBoardIdentityChecks_HubMark_FollowsIdentityChecks(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	report := appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	require.GreaterOrEqual(t, len(report.Checks), 2)
	assert.Equal(t, "board.hub-mark", report.Checks[len(report.Checks)-1].Name)
}

// The doctor's "board connected" answer and the sticky mark never disagree:
// whenever the mark service reports a connection, so does the doctor.
func TestIdentityBoardConnected_AgreesWithHubMark(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
		want  bool
	}{
		{"local board", func(t *testing.T) {}, false},
		{"mark set then evidence removed", func(t *testing.T) {
			flipped, err := service.NewHubMarkService(app.store).RecordEnvConnection(context.Background())
			require.NoError(t, err)
			require.True(t, flipped)
		}, true},
		{"applied rows from an older applier", func(t *testing.T) {
			require.NoError(t, app.store.WithTx(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO applied_events(event_id, applied_at, applied_by_lamport) VALUES('e1','2026-01-01T00:00:00Z',1)`)
				return err
			}))
		}, true},
		{"secrets dsn present", func(t *testing.T) {
			require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, transport.SecretsFilename), []byte("postgres://placeholder\n"), 0o600))
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			t.Setenv(transport.EnvDSN, "")
			tt.setup(t)
			status, err := service.NewHubMarkService(app.store).Inspect(context.Background(), collectHubEvidence())
			require.NoError(t, err)
			assert.Equal(t, tt.want, status.Connected)
			assert.Equal(t, status.Connected, identityBoardConnected(oplocal.BoardObservation{}),
				"doctor and the hub mark must agree")
		})
	}
}

func TestCollectHubEvidence_ConfigAndSecrets_AreSeen(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	assert.Equal(t, service.HubEvidence{}, collectHubEvidence(), "a local board has no evidence")

	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, transport.SecretsFilename), []byte("postgres://placeholder\n"), 0o600))
	assert.True(t, collectHubEvidence().SecretsDSN)
}

func TestCollectHubEvidence_ExportedDSN_IsNotEvidence(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "postgres://exported.invalid/db")
	assert.False(t, collectHubEvidence().EnvDSNConnected, "a DSN that was never used to connect proves nothing")
	assert.False(t, collectHubEvidence().SecretsDSN)
}

func TestDoctorHubMark_LocalBoard_ReportsNotSet(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	check := hubMarkCheck(t)
	assert.True(t, check.Pass)
	assert.Contains(t, check.Detail, "not set")
	assert.Empty(t, check.Fix)
}

func TestDoctorHubMark_AfterConnection_ReportsSource(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "postgres://exported.invalid/db")
	var stderr bytes.Buffer
	noteHubConnection(context.Background(), &stderr)
	assert.Empty(t, stderr.String())

	// The DSN is gone and no config remains; the mark is sticky.
	t.Setenv(transport.EnvDSN, "")
	check := hubMarkCheck(t)
	assert.True(t, check.Pass)
	assert.Contains(t, check.Detail, "set")
	assert.Contains(t, check.Detail, "env_dsn")
}

func TestNoteHubConnection_EnvDSN_MirrorsOperatorLocal(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	t.Setenv(transport.EnvDSN, "postgres://exported.invalid/db")
	noteHubConnection(context.Background(), &bytes.Buffer{})
	mirror, _, err := state.HubMarkMirror(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	assert.Equal(t, oplocal.HubMarkMirror{Checkout: filepath.Dir(app.mtixDir), Source: "env_dsn"}, mirror)
}

func TestNoteHubConnection_SecretsDSN_RecordsSecretsSource(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	noteHubConnection(context.Background(), &bytes.Buffer{})
	check := hubMarkCheck(t)
	assert.Contains(t, check.Detail, "secrets")
}

func TestNoteHubConnection_NoStore_IsNoop(t *testing.T) {
	initTestApp(t)
	require.NoError(t, app.store.Close())
	app.store = nil
	var stderr bytes.Buffer
	noteHubConnection(context.Background(), &stderr)
	assert.Empty(t, stderr.String())
}

func TestDoctorHubMark_JSON_CarriesNameAndDetail(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	var out, diagnostic bytes.Buffer
	app.jsonOutput = true
	_ = runSyncDoctor(t.Context(), &out, &diagnostic, nil, transport.Options{})
	var report DoctorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	found := false
	for _, check := range report.Checks {
		if check.Name == "board.hub-mark" {
			found = true
			assert.True(t, check.Pass)
			assert.NotEmpty(t, check.Detail)
		}
	}
	assert.True(t, found)
}

func TestCollectHubEvidence_ConfigKeys_Table(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
		want bool
	}{
		{"sync enabled true", "sync.enabled", "true", true},
		{"sync enabled false", "sync.enabled", "false", false},
		{"sync endpoint set", "sync.endpoint", "https://hub.example.invalid", true},
		{"relay dir set", "sync.relay.dir", "relay-dir", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			t.Setenv(transport.EnvDSN, "")
			assert.False(t, collectHubEvidence().ConfigKeys, "defaults are not evidence")
			_, err := app.configSvc.Set(tt.key, tt.val)
			require.NoError(t, err)
			assert.Equal(t, tt.want, collectHubEvidence().ConfigKeys)
		})
	}
}

// An exported DSN alone is not evidence for the doctor either: the board.id
// "connected" answer and the hub mark agree (ADR-007 A9).
func TestIdentityBoardConnected_ExportedDSNOnly_AgreesWithHubMark(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "postgres://exported.invalid/db")
	assert.False(t, identityBoardConnected(oplocal.BoardObservation{}))
	assert.False(t, hubMarkConnected())
	assert.Contains(t, hubMarkCheck(t).Detail, "not set")
}

func TestIdentityBoardConnected_RecordedObservation_IsConnected(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	assert.True(t, identityBoardConnected(oplocal.BoardObservation{Checkout: "/x"}),
		"a hub observation a pull recorded is evidence of its own")
}

func TestHubMarkMirror_FollowsStore_NeverDrifts(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	state := identityOperatorState(t)
	checkout := filepath.Dir(app.mtixDir)
	ctx := context.Background()
	mirror := func() oplocal.HubMarkMirror {
		m, _, err := state.HubMarkMirror(checkout)
		require.NoError(t, err)
		return m
	}

	// A flip the store made on its own (a foreign event applied in
	// IdempotentApply, an inference) reaches the mirror on reconcile.
	_, err := app.store.MarkHubConnected(ctx, "foreign_event")
	require.NoError(t, err)
	assert.Equal(t, oplocal.HubMarkMirror{}, mirror())
	require.NoError(t, reconcileHubMarkMirror())
	assert.Equal(t, oplocal.HubMarkMirror{Checkout: checkout, Source: "foreign_event"}, mirror())

	// A reset clears the store mark and the mirror together.
	var stderr bytes.Buffer
	require.NoError(t, resetHubMark(ctx, &stderr))
	assert.Equal(t, oplocal.HubMarkMirror{}, mirror())
	mark, err := app.store.HubMark(ctx)
	require.NoError(t, err)
	assert.False(t, mark.Set)
	require.NoError(t, reconcileHubMarkMirror())
	assert.Equal(t, oplocal.HubMarkMirror{}, mirror())
}

func TestShouldReconcileHubMark_SyncRelayAndDaemonCommands(t *testing.T) {
	tests := map[string]bool{
		"mtix sync pull": true, "mtix sync relay pass": true, "mtix daemon": true,
		"mtix sync daemon": true, "mtix list": false, "mtix create": false,
	}
	for path, want := range tests {
		assert.Equal(t, want, shouldReconcileHubMark(path), path)
	}
}

func TestReconcileHubMarkMirror_NoStore_IsNoop(t *testing.T) {
	initTestApp(t)
	require.NoError(t, app.store.Close())
	app.store = nil
	require.NoError(t, reconcileHubMarkMirror())
}

func TestAppendHubMarkCheck_UnreadableStore_FailsWithRemedy(t *testing.T) {
	initTestApp(t)
	require.NoError(t, app.store.Close())
	report := appendHubMarkCheck(context.Background(), DoctorReport{OverallPass: true})
	check := findCheck(t, report, "board.hub-mark")
	assert.False(t, check.Pass)
	assert.False(t, report.OverallPass)
	assert.NotEmpty(t, check.Fix)
}

func TestAppendHubMarkCheck_NoStore_Warns(t *testing.T) {
	initTestApp(t)
	require.NoError(t, app.store.Close())
	app.store = nil
	check := findCheck(t, appendHubMarkCheck(context.Background(), DoctorReport{OverallPass: true}), "board.hub-mark")
	assert.True(t, check.Pass)
	assert.True(t, check.Warn)
	assert.False(t, hubMarkConnected())
}

func TestNoteHubConnection_StoreError_WarnsAndContinues(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "postgres://exported.invalid/db")
	require.NoError(t, app.store.Close())
	var stderr bytes.Buffer
	noteHubConnection(context.Background(), &stderr)
	assert.Contains(t, stderr.String(), "warning: could not record the hub-connected mark")
}

func TestMirrorHubMark_UnusableOperatorState_WarnsOnly(t *testing.T) {
	initTestApp(t)
	t.Setenv("XDG_CONFIG_HOME", "relative-not-absolute")
	t.Setenv("APPDATA", "relative-not-absolute")
	var stderr bytes.Buffer
	t.Setenv(transport.EnvDSN, "postgres://exported.invalid/db")
	noteHubConnection(context.Background(), &stderr)
	assert.Contains(t, stderr.String(), "warning: could not mirror the hub-connected mark")
}

func TestResetHubMark_NoStoreOrClosedStore_ReportsError(t *testing.T) {
	initTestApp(t)
	require.NoError(t, app.store.Close())
	require.ErrorContains(t, resetHubMark(context.Background(), &bytes.Buffer{}), "reset hub mark")
	app.store = nil
	require.ErrorContains(t, resetHubMark(context.Background(), &bytes.Buffer{}), "not initialized")
}

func TestReconcileHubMarkMirror_StoreOrStateErrors_AreWrapped(t *testing.T) {
	initTestApp(t)
	t.Setenv("XDG_CONFIG_HOME", "relative-not-absolute")
	t.Setenv("APPDATA", "relative-not-absolute")
	require.ErrorContains(t, reconcileHubMarkMirror(), "reconcile hub mark mirror")
	require.NoError(t, app.store.Close())
	require.ErrorContains(t, reconcileHubMarkMirror(), "reconcile hub mark mirror")
}

func TestResetHubMark_MirrorFailure_WarnsAfterStoreReset(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	t.Setenv("XDG_CONFIG_HOME", "relative-not-absolute")
	t.Setenv("APPDATA", "relative-not-absolute")
	var stderr bytes.Buffer
	require.NoError(t, resetHubMark(context.Background(), &stderr))
	assert.Contains(t, stderr.String(), "could not clear the hub-connected mirror")
}

// Config evidence is persisted by doctor and by the sync commands, so the
// mark is sticky for it as well (ADR-007 §3.5): removing the keys later does
// not unmark the board, a reset clears it, and fresh evidence marks again.
func TestHubMark_ConfigEvidence_PersistsAndSurvivesKeyRemoval(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	state := identityOperatorState(t)
	checkout := filepath.Dir(app.mtixDir)
	ctx := context.Background()
	stored := func() (bool, string) {
		mark, err := app.store.HubMark(ctx)
		require.NoError(t, err)
		return mark.Set, mark.Source
	}

	set, _ := stored()
	require.False(t, set)
	_, err := app.configSvc.Set("sync.enabled", "true")
	require.NoError(t, err)
	check := hubMarkCheck(t)
	assert.Contains(t, check.Detail, "source: config")
	set, source := stored()
	assert.True(t, set, "doctor persists the mark it reports as sticky")
	assert.Equal(t, "config", source)
	mirror, _, err := state.HubMarkMirror(checkout)
	require.NoError(t, err)
	assert.Equal(t, "config", mirror.Source, "the mirror follows the store")

	// The keys go away; the mark stays.
	_, err = app.configSvc.Set("sync.enabled", "false")
	require.NoError(t, err)
	assert.Contains(t, hubMarkCheck(t).Detail, "source: config")
	set, _ = stored()
	assert.True(t, set)

	// A reset clears it and it stays cleared until fresh evidence appears.
	require.NoError(t, resetHubMark(ctx, &bytes.Buffer{}))
	assert.Contains(t, hubMarkCheck(t).Detail, "not set")
	set, _ = stored()
	assert.False(t, set)
	_, err = app.configSvc.Set("sync.enabled", "true")
	require.NoError(t, err)
	assert.Contains(t, hubMarkCheck(t).Detail, "source: config")
	set, _ = stored()
	assert.True(t, set)
}

func TestNoteHubConnection_ConfigEvidenceAndEnvDSN_MarkWithConfigSource(t *testing.T) {
	initTestApp(t)
	_, err := app.configSvc.Set("sync.enabled", "true")
	require.NoError(t, err)
	t.Setenv(transport.EnvDSN, "")
	noteHubConnection(context.Background(), &bytes.Buffer{})
	mark, err := app.store.HubMark(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "config", mark.Source)
}

func TestHubMarkCheck_UnpersistableMark_FailsWithRemedy(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	_, err := app.configSvc.Set("sync.enabled", "true")
	require.NoError(t, err)
	require.NoError(t, app.store.Close())
	check := hubMarkCheck(t)
	assert.False(t, check.Pass)
	assert.NotEmpty(t, check.Fix)
}
