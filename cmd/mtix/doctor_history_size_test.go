// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestDoctor_HistorySizeCheck_ReportsRowsLargestTasksAndLimitInJSON(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	changePriorityTimes(t, id, 3)
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: true}))
	var rows []struct {
		ID, Status, Detail, Fix string
		Data                    map[string]any
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), out.String())
	var found bool
	for _, r := range rows {
		if r.ID != "history.size" {
			continue
		}
		found = true
		assert.Equal(t, "info", r.Status)
		assert.GreaterOrEqual(t, r.Data["rows"], float64(3))
		assert.Contains(t, r.Data, "largest_tasks")
		assert.Contains(t, r.Data, "max_entries_per_task")
		assert.Contains(t, r.Data, "oversize_tasks")
	}
	require.True(t, found, out.String())
}

func TestDoctor_HistorySizeCheck_WarnsWhenTasksExceedTheLimit(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	changePriorityTimes(t, id, 6)
	_, err := app.configSvc.Set("history.max_entries_per_task", "2")
	require.NoError(t, err)
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: true}))
	assert.Contains(t, out.String(), `"id":"history.size","status":"warn"`)
	assert.Contains(t, out.String(), "oversize_tasks")
}

// The size is computed once per doctor run: the registration decision and the
// row share one report, and the fix does not suggest editing tasks.
func TestDoctor_HistorySizeCheck_SharesOneReportAndWordsTheFixWithoutDummyEdits(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	changePriorityTimes(t, id, 6)
	_, err := app.configSvc.Set("history.max_entries_per_task", "2")
	require.NoError(t, err)
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	changePriorityTimes(t, id, 1) // after registration: a second computation would see this row
	var out bytes.Buffer
	require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: true}))
	var rows []struct {
		ID, Fix string
		Data    map[string]any
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	for _, r := range rows {
		if r.ID != "history.size" {
			continue
		}
		before := r.Data["rows"].(float64)
		assert.GreaterOrEqual(t, before, float64(6))
		assert.NotContains(t, r.Fix, "make a change")
		assert.Contains(t, r.Fix, "next change")
		return
	}
	t.Fatal("no history.size row")
}

// A hand-edited limit that is not a positive whole number is read as unset;
// doctor says so, naming the key and the exact fix.
func TestDoctor_HistorySizeCheck_UnreadableHandEditedLimit_WarnsNamingTheKeyAndFix(t *testing.T) {
	for _, bad := range []string{"ten", "0", "-3"} {
		t.Run(bad, func(t *testing.T) {
			initTestApp(t)
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte("history.max_entries_per_task: "+bad+"\n"), 0o600))
			cs, err := service.NewConfigService(path)
			require.NoError(t, err)
			app.configSvc = cs
			svc, err := newConfiguredDoctorService()
			require.NoError(t, err)
			var out bytes.Buffer
			require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: true}))
			var rows []struct{ ID, Status, Detail, Fix string }
			require.NoError(t, json.Unmarshal(out.Bytes(), &rows), out.String())
			var found bool
			for _, r := range rows {
				if r.ID != "history.size" {
					continue
				}
				found = true
				assert.Equal(t, "warn", r.Status)
				assert.Contains(t, r.Detail, "history.max_entries_per_task")
				assert.Contains(t, r.Fix, "mtix config set history.max_entries_per_task")
			}
			require.True(t, found, out.String())
		})
	}
}
