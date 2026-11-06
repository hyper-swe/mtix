// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.17.16.16: mtix doctor lists the tasks an automatic import changed
// that the hub knows and whose change stayed local, with the exact fix, and
// the check passes once the fix has been run.

func localOnlyCheck(t *testing.T) DoctorCheck {
	t.Helper()
	r := appendLocalOnlyImportCheck(context.Background(), DoctorReport{OverallPass: true}, app.store)
	require.Len(t, r.Checks, 1)
	require.True(t, r.OverallPass, "a WARN never fails the report")
	return r.Checks[0]
}

// TestDoctor_LocalOnlyImports_WarnsWithBoundedJSONListAndFix: the check is a
// WARN with the count, at most ten ids in local_only, and the fix; with no
// record it passes quietly.
func TestDoctor_LocalOnlyImports_WarnsWithBoundedJSONListAndFix(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.True(t, localOnlyCheck(t).Pass)
	require.False(t, localOnlyCheck(t).Warn)
	var ids []string
	for i := 1; i <= 12; i++ {
		require.NoError(t, runCreate(fmt.Sprintf("task %d", i), "", "", 3, "", "", "", "", ""))
		ids = append(ids, fmt.Sprintf("TEST-%d", i))
	}
	require.NoError(t, app.store.RecordLocalOnlyImports(ctx, onlyIDs(ids)))
	// a bare id settles on any later event of the task (the record of an older build)

	check := localOnlyCheck(t)

	require.True(t, check.Warn)
	require.Contains(t, check.Detail, "12 task(s)")
	require.Len(t, check.LocalOnly, sqlite.MaxNamedNodes)
	require.Contains(t, check.Fix, "mtix update <id>")
	require.NotContains(t, check.Fix, "run mtix sync pull", "a pull takes the hub value only when the hub holds a later edit")
	require.Contains(t, check.Fix, "no command does it on demand")
	raw, err := json.Marshal(check)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"local_only":["TEST-1"`)
}

// TestDoctor_LocalOnlyImports_FixClearsTheTask: editing the task again, the
// first fix the check names, journals a later event and the task drops off.
func TestDoctor_LocalOnlyImports_FixClearsTheTask(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Imported", "", "", 3, "", "", "", "", ""))
	require.NoError(t, app.store.RecordLocalOnlyImports(context.Background(), onlyIDs([]string{"TEST-1"})))
	require.True(t, localOnlyCheck(t).Warn)

	require.NoError(t, runUpdate("TEST-1", "Imported", "", "", "", 0, "", ""))

	require.False(t, localOnlyCheck(t).Warn, "the update journaled the current value")
}

func onlyIDs(ids []string) []sqlite.LocalOnlyImport {
	out := make([]sqlite.LocalOnlyImport, 0, len(ids))
	for _, id := range ids {
		out = append(out, sqlite.LocalOnlyImport{ID: id})
	}
	return out
}

// TestDoctor_LocalOnlyImports_FixSettlesOneFieldAtATime: the printed route
// for a field, run on the real update path, clears that field and nothing
// else; the task drops off once every listed field has been edited
// (MTIX-112.17.16.16, principle C).
func TestDoctor_LocalOnlyImports_FixSettlesOneFieldAtATime(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Imported", "", "", 3, "", "", "", "", ""))
	require.NoError(t, app.store.RecordLocalOnlyImports(context.Background(),
		[]sqlite.LocalOnlyImport{{ID: "TEST-1", Fields: []string{"title", "priority"}}}))
	check := localOnlyCheck(t)
	require.Contains(t, check.Detail, "TEST-1 (priority, title)")
	require.Contains(t, check.Fix, `title: mtix update <id> --title`)
	require.Contains(t, check.Fix, `priority: mtix update <id> --priority`)

	require.NoError(t, runUpdate("TEST-1", "Imported", "", "", "", 0, "", ""), "the title route")
	check = localOnlyCheck(t)
	require.True(t, check.Warn)
	require.Contains(t, check.Detail, "TEST-1 (priority)")

	require.NoError(t, runUpdate("TEST-1", "", "", "", "", 2, "", ""), "the priority route")
	require.False(t, localOnlyCheck(t).Warn)
}

// TestDoctorSyncRows_LocalOnlyIDs_AreInTheRowData: mtix doctor --json carries
// the bounded id list in the row's data, not only in the detail prose.
func TestDoctorSyncRows_LocalOnlyIDs_AreInTheRowData(t *testing.T) {
	rows := doctorSyncRows(DoctorReport{Checks: []DoctorCheck{{
		Name: localOnlyImportCheckName, Pass: true, Warn: true, Detail: "d", Fix: "f", LocalOnly: []string{"TEST-1", "TEST-2"},
	}}})

	require.Len(t, rows, 1)
	require.Equal(t, []string{"TEST-1", "TEST-2"}, rows[0].Data["local_only"])
}
