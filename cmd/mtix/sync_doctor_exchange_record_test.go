// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Local doctor reports received provenance without contacting a hub.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestDoctorExchangeRecord_ReportsPresence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("receipt publication is unavailable on this platform")
	}
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o755))
	st, err := sqlite.New(filepath.Join(dir, "data"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	clock := func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	st.SetClock(clock)
	incoming, err := st.Export(ctx, "TEST", "0.6.0")
	require.NoError(t, err)
	raw, err := json.Marshal(incoming)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks.json"), raw, 0o644))
	svc := service.NewSyncService(st, slog.Default(), clock)
	require.NoError(t, svc.AutoImport(ctx, dir))
	prior := app.mtixDir
	app.mtixDir = dir
	t.Cleanup(func() { app.mtixDir = prior })
	report := appendLocalStoreChecks(ctx, DoctorReport{OverallPass: true}, st)
	var found *DoctorCheck
	for i := range report.Checks {
		if report.Checks[i].Name == "sync.exchange-record" {
			found = &report.Checks[i]
		}
	}
	require.NotNil(t, found, "doctor includes local received provenance")
	require.True(t, found.Pass)
	require.Contains(t, found.Detail, "2026-10-01T00:00:00Z")
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "sync.exchange-record")
}

func TestDoctorExchangeRecord_MissingAndOmission_ReadOnlyDiagnostics(t *testing.T) {
	initTestApp(t)
	original := app.mtixDir
	app.mtixDir = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { app.mtixDir = original })
	check := appendExchangeRecordCheck(t.Context(), DoctorReport{OverallPass: true}, app.store).Checks[0]
	require.True(t, check.Pass)
	require.True(t, check.Warn)
	require.Contains(t, check.Detail, "no received record")
	_, err := os.Stat(app.mtixDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	var text bytes.Buffer
	printDoctorTable(&text, DoctorReport{OverallPass: true, Checks: []DoctorCheck{check}})
	require.Contains(t, text.String(), "sync.exchange-record")
	status, err := readExchangeStatus(t.Context(), app.store)
	require.NoError(t, err)
	require.False(t, status.Present)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	failed := appendExchangeRecordCheck(ctx, DoctorReport{OverallPass: true}, app.store).Checks[0]
	require.False(t, failed.Pass)
	noStore := appendExchangeRecordCheck(t.Context(), DoctorReport{OverallPass: true}, nil).Checks[0]
	require.True(t, noStore.Warn)
}

func TestDoctorExchangeRecord_HubFailureAndConfiguredCap_KeepLocalCheck(t *testing.T) {
	initTestApp(t)
	report := DoctorReport{OverallPass: false, Checks: []DoctorCheck{{Name: "PG reachable", Pass: false}}}
	report = appendLocalChecks(t.Context(), report)
	var found bool
	for _, check := range report.Checks {
		if check.Name == "sync.exchange-record" {
			found = true
		}
	}
	require.True(t, found)
	app.syncSvc.MaxImportSize = 1
	status, err := readExchangeStatus(t.Context(), app.store)
	require.NoError(t, err)
	require.False(t, status.Present)
	require.Same(t, app.syncSvc, exchangeService(app.store))
}
