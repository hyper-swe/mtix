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
	require.NoError(t, app.store.RecordLocalOnlyImports(ctx, ids))

	check := localOnlyCheck(t)

	require.True(t, check.Warn)
	require.Contains(t, check.Detail, "12 task(s)")
	require.Len(t, check.LocalOnly, sqlite.MaxNamedNodes)
	require.Contains(t, check.Fix, "mtix update <id>")
	require.Contains(t, check.Fix, "mtix sync pull")
	raw, err := json.Marshal(check)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"local_only":["TEST-1"`)
}

// TestDoctor_LocalOnlyImports_FixClearsTheTask: editing the task again, the
// first fix the check names, journals a later event and the task drops off.
func TestDoctor_LocalOnlyImports_FixClearsTheTask(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Imported", "", "", 3, "", "", "", "", ""))
	require.NoError(t, app.store.RecordLocalOnlyImports(context.Background(), []string{"TEST-1"}))
	require.True(t, localOnlyCheck(t).Warn)

	require.NoError(t, runUpdate("TEST-1", "Imported", "", "", "", 0, "", ""))

	require.False(t, localOnlyCheck(t).Warn, "the update journaled the current value")
}
