// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.17.16.16: mtix import --mode merge says how many sync events it
// journaled on a store that syncs, and nothing on one that does not.

// TestRunImport_MergeOnSyncedStore_ReportsJournaledEvents: the result line
// names the journal and the push that sends it; a store that has not synced
// prints no such line.
func TestRunImport_MergeOnSyncedStore_ReportsJournaledEvents(t *testing.T) {
	for _, synced := range []bool{true, false} {
		name := "unsynced"
		if synced {
			name = "synced"
		}
		t.Run(name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Seed", "", "", 3, "", "", "", "", ""))
			if synced {
				_, err := app.store.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed'`)
				require.NoError(t, err)
			}
			path := mergeFileRetitling(t, "TEST-1", "Imported title", "")

			out := captureStdout(t, func() { require.NoError(t, runImport(path, importFlags{mode: "merge", force: true})) })

			if synced {
				require.Contains(t, out, "Journaled 1 sync events for the changes; mtix sync push sends them to the hub")
			} else {
				require.NotContains(t, out, "Journaled")
			}
		})
	}
}

// TestRunImport_MergeChangingAColumnNoEventCarries_PrintsTheLocalOnlyLine: a
// merge on a store that syncs that also changes the weight prints the task
// and the column as local only, which the journal line does not cover.
func TestRunImport_MergeChangingAColumnNoEventCarries_PrintsTheLocalOnlyLine(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Seed", "", "", 3, "", "", "", "", ""))
	_, err := app.store.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed'`)
	require.NoError(t, err)
	data, err := app.store.Export(t.Context(), "TEST", "")
	require.NoError(t, err)
	data.Nodes[0].Title, data.Nodes[0].Weight = "Imported title", 2.5
	data.Nodes[0].ContentHash = model.ComputeContentHash("Imported title", "", "", "", nil)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "merge.json")
	require.NoError(t, os.WriteFile(path, raw, 0o644))

	out := captureStdout(t, func() { require.NoError(t, runImport(path, importFlags{mode: "merge", force: true})) })

	require.Contains(t, out, "Local only (no sync event carries these columns, so mtix sync push does not send them): TEST-1 (weight)")
}

// TestDoctor_HeldPushCheck_ReportsLocalOnlyAndQueueCapChecks: the doctor's
// sync report carries the local-only and queue-cap checks, so an agent that
// runs mtix doctor sees them.
func TestDoctor_HeldPushCheck_ReportsLocalOnlyAndQueueCapChecks(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Seed", "", "", 3, "", "", "", "", ""))
	require.NoError(t, app.store.RecordLocalOnlyImports(t.Context(), onlyIDs([]string{"TEST-1"})))

	r := appendHeldPushCheck(t.Context(), DoctorReport{OverallPass: true}, app.store)

	names := map[string]DoctorCheck{}
	for _, c := range r.Checks {
		names[c.Name] = c
	}
	require.True(t, names[localOnlyImportCheckName].Warn)
	require.Contains(t, names, queueCapCheckName)
}
