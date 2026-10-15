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

func historySizeRowOf(t *testing.T) (row struct {
	ID, Status, Detail, Fix string
	Data                    map[string]any
}) {
	t.Helper()
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: true}))
	var rows []struct {
		ID, Status, Detail, Fix string
		Data                    map[string]any
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), out.String())
	for _, r := range rows {
		if r.ID == "history.size" {
			return r
		}
	}
	require.Fail(t, "no history.size row", out.String())
	return row
}

// Change entries dated after the clock are counted in the data object and
// named in the detail; an exempt kind dated ahead is not counted.
func TestDoctor_HistorySizeCheck_EntriesDatedAhead_AreCountedInDataAndDetail(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	changePriorityTimes(t, id, 1)
	var uid string
	require.NoError(t, app.store.WriteDB().QueryRow(`SELECT task_uid FROM task_history LIMIT 1`).Scan(&uid))
	for i, kind := range []string{"change", "change", "redaction"} {
		_, err := app.store.WriteDB().Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field)
			VALUES (?, ?, '2999-01-01T00:00:00.000000000Z', 'a', 'local', ?, 'x')`, "01U000000000000000000AHEA"+string(rune('A'+i)), uid, kind)
		require.NoError(t, err)
	}
	row := historySizeRowOf(t)
	assert.Equal(t, float64(2), row.Data["ahead_of_clock"])
	assert.Equal(t, float64(0), row.Data["cutoffs_ahead_of_clock"])
	assert.Contains(t, row.Detail, "2 change entries are dated after this machine's clock")
}

// A retention cutoff later than the clock is a warning with the fix.
func TestDoctor_HistorySizeCheck_CutoffLaterThanTheClock_WarnsWithTheFix(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	changePriorityTimes(t, id, 1)
	_, err := app.store.WriteDB().Exec(`INSERT INTO history_retention_cutoff (task_uid, cutoff_ts, updated_at)
		VALUES ('some-uid', '2999-01-01T00:00:00.000000000Z', '2999-01-01T00:00:00.000000000Z')`)
	require.NoError(t, err)
	row := historySizeRowOf(t)
	assert.Equal(t, "warn", row.Status)
	assert.Equal(t, float64(1), row.Data["cutoffs_ahead_of_clock"])
	assert.Contains(t, row.Detail, "1 retention cutoffs are later than this machine's clock")
	assert.Contains(t, row.Fix, "set the system clock right")
}
