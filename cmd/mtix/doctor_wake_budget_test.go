// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func budgetDoctorOperatorSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(os.DirFS(root), filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		files[relative] = strconv.FormatUint(uint64(info.Mode()), 8) + ":" + string(data)
		return nil
	})
	require.NoError(t, err)
	return files
}

func TestDoctorBudgetRewind_TextJSON_ReadOnly(t *testing.T) {
	initTestApp(t)
	state := testutil.OperatorState(t)
	t.Setenv("MTIX_WAKE_HOP", "0")
	seedThreeAddressed(t, "worker")
	sent := 0
	d, keys := cliBackoffDispatcher(t, app.store, state, &sent)
	require.NoError(t, d.Pass(t.Context(), keys[:2]))
	_, err := app.store.WriteDB().ExecContext(t.Context(), "UPDATE wake_budget_counters SET total=? WHERE agent_id=?", 0, "worker")
	require.NoError(t, err)
	dir, err := state.Path()
	require.NoError(t, err)
	before := budgetDoctorOperatorSnapshot(t, dir)
	for _, jsonOutput := range []bool{false, true} {
		svc, err := newConfiguredDoctorService()
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: jsonOutput, Fix: true}))
		require.Contains(t, out.String(), "budget.rewind")
		if jsonOutput {
			var rows []service.DoctorResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
			for _, row := range rows {
				if row.ID == "budget.rewind" {
					require.Equal(t, service.DoctorWarn, row.Status)
					require.Contains(t, row.Detail, "protected")
				}
			}
		}
	}
	require.Equal(t, before, budgetDoctorOperatorSnapshot(t, dir))
	var total int64
	require.NoError(t, app.store.ReadDB().QueryRowContext(t.Context(), "SELECT total FROM wake_budget_counters WHERE agent_id=?", "worker").Scan(&total))
	require.Zero(t, total, "doctor --fix never heals or resets wake accounting")
}

func TestDoctorBudgetRewind_NoAccounting_DoesNotCreateState(t *testing.T) {
	initTestApp(t)
	state := testutil.OperatorState(t)
	dir, err := state.Path()
	require.NoError(t, err)
	_, err = os.Stat(dir)
	require.ErrorIs(t, err, fs.ErrNotExist)
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: true, Fix: true}))
	require.Contains(t, out.String(), "budget.rewind")
	_, err = os.Stat(dir)
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestSyncDoctorBudgetRewind_CollectedWithoutOperatorWrites(t *testing.T) {
	initTestApp(t)
	state := testutil.OperatorState(t)
	dir, err := state.Path()
	require.NoError(t, err)
	report := appendLocalChecks(t.Context(), DoctorReport{OverallPass: true})
	found := false
	for _, row := range report.Checks {
		found = found || row.Name == "budget.rewind"
	}
	require.True(t, found)
	_, err = os.Stat(dir)
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestDoctorBudgetRewind_MissingHomeReportsEnvironmentFailure(t *testing.T) {
	initTestApp(t)
	testutil.OperatorState(t)
	key := "HOME"
	if runtime.GOOS == "windows" {
		key = "USERPROFILE"
	}
	t.Setenv(key, "")
	row := configuredWakeBudgetRow(t.Context(), app.store)
	require.Equal(t, service.DoctorFail, row.Status)
	require.Contains(t, row.Detail, "environment is unavailable")
	require.Contains(t, row.Fix, "mtix doctor --json")
}

func TestDoctorBudgetRewind_NoStoreDoesNotRegisterAccounting(t *testing.T) {
	initTestApp(t)
	testutil.OperatorState(t)
	store := app.store
	app.store = nil
	defer func() { app.store = store }()
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, registerConfiguredWakeBudgetDoctor(svc))
	rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
	require.NoError(t, err)
	require.Len(t, rows, 1, "only the operator-local guard-trips row registers without a store")
	require.Equal(t, "wake.guard-trips", rows[0].ID)
}
