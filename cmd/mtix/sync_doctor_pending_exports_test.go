// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestSyncDoctor_StalePendingExport_Warns(t *testing.T) {
	for _, jsonOutput := range []bool{true, false} {
		t.Run(map[bool]string{true: "json", false: "text"}[jsonOutput], func(t *testing.T) {
			pendingExportDoctorFixture(t)
			app.jsonOutput = jsonOutput
			var out bytes.Buffer
			err := runSyncDoctor(context.Background(), &out, nil, transport.Options{})
			require.ErrorIs(t, err, errDoctorChecksFailed, "existing no-DSN hub checks fail independently of the new local warning")
			if jsonOutput {
				assertPendingExportDoctorJSON(t, out.Bytes())
				return
			}
			require.Contains(t, out.String(), "[WARN] pending exports")
			require.Contains(t, out.String(), "pending exports: 1")
			require.Contains(t, out.String(), "oldest")
			require.Contains(t, out.String(), "fix: mtix list")
			require.NotContains(t, out.String(), "fix: mtix sync --fix", "doctor must not prescribe discarding a pulled board")
		})
	}
}

func pendingExportDoctorFixture(t *testing.T) {
	t.Helper()
	saveAndResetApp(t)
	t.Setenv(transport.EnvDSN, "")
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0755))
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	store, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	clock := old
	store.SetClock(func() time.Time { return clock })
	svc := service.NewSyncService(store, logger, func() time.Time { return clock })
	app.store, app.syncSvc, app.mtixDir, app.logger = store, svc, dir, logger
	require.NoError(t, svc.RequestExport(dir))
	entries, err := os.ReadDir(filepath.Join(dir, "data", "export-pending"))
	if err != nil {
		require.ErrorIs(t, err, os.ErrNotExist, "coalesced metadata needs no request directory")
	} else {
		for _, entry := range entries {
			if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "request-") {
				path := filepath.Join(dir, "data", "export-pending", entry.Name())
				require.NoError(t, os.Chtimes(path, old, old))
			}
		}
	}
	clock = now // No sleep: native legacy mtime and the instance clock define a precise two-hour age.
}

func assertPendingExportDoctorJSON(t *testing.T, raw []byte) {
	t.Helper()
	check := doctorCheckFromJSON(t, raw, "pending exports")
	require.True(t, check.Pass)
	require.True(t, check.Warn)
	require.Equal(t, "mtix list", check.Fix)
	var report struct {
		Checks []struct {
			Name    string `json:"name"`
			Pending struct {
				Count   int    `json:"count"`
				Oldest  string `json:"oldest_at"`
				Seconds int64  `json:"oldest_age_seconds"`
			} `json:"pending_exports"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(raw, &report))
	for _, row := range report.Checks {
		if row.Name == "pending exports" {
			require.Equal(t, 1, row.Pending.Count)
			require.Equal(t, "2026-10-11T10:00:00Z", row.Pending.Oldest)
			require.Equal(t, int64(7200), row.Pending.Seconds)
			return
		}
	}
	t.Fatal("pending-export numeric diagnostics missing from native JSON")
}
