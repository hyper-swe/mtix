// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"regexp"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestRunImport_CapRefusal_PrintedFixClearsIt: on a store that syncs, the
// cap refusal names mtix config set sync.max_queue_size with the value the
// import needs, and running exactly that command lets the same import run on
// the next invocation (MTIX-112.17.16.16, principle C). The events stay
// pending throughout, as on a relay-only board, so a push is not the fix.
func TestRunImport_CapRefusal_PrintedFixClearsIt(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Seed", "", "", 3, "", "", "", "", ""))
	_, err := app.store.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed'`)
	require.NoError(t, err)
	require.NoError(t, runConfigSet("sync.max_queue_size", "1"))
	require.NoError(t, initApp(&cobra.Command{Use: "test"}, "")) // the next invocation reads the config
	path := mergeFileRetitling(t, "TEST-1", "Imported title", "Imported description")

	err = runImport(path, importFlags{mode: "merge", force: true})

	require.ErrorIs(t, err, model.ErrSyncQueueFull)
	fix := regexp.MustCompile(`mtix config set (sync\.max_queue_size) (\d+)`).FindStringSubmatch(err.Error())
	require.NotNil(t, fix, err.Error())
	require.Equal(t, "2", fix[2], "two events on an empty queue")
	require.NoError(t, runConfigSet(fix[1], fix[2]), "the printed fix")
	require.NoError(t, initApp(&cobra.Command{Use: "test"}, ""))
	require.NoError(t, runImport(path, importFlags{mode: "merge", force: true}), "the same import runs after the fix")
}
