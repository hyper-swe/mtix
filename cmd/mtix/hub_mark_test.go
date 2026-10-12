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
