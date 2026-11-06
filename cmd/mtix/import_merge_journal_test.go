// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
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
