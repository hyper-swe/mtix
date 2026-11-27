// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestImport_TailEventRetained_PreservesDestinationAck(t *testing.T) {
	for _, cumulative := range []bool{false, true} {
		t.Run(map[bool]string{false: "selective", true: "cumulative"}[cumulative], func(t *testing.T) {
			dest := newTestStore(t)
			_, _, last := addressThree(t, dest, "recipient")
			// A valid explicit export can retain addressed mail after its task has
			// disappeared from the board. Its older ack coverage must not undo ours.
			incoming := transportExport(t, dest)
			incoming = withoutExportNode(incoming, "PROJ-3")
			require.NoError(t, sqlite.RecomputeExportChecksum(incoming))
			if cumulative {
				require.NoError(t, dest.InboxAckThrough(t.Context(), "recipient", last))
			} else {
				require.NoError(t, dest.InboxAck(t.Context(), "recipient", last))
			}
			before, err := dest.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			_, err = dest.Import(t.Context(), incoming, sqlite.ImportModeReplace, false)
			require.NoError(t, err)
			after, err := dest.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			require.Len(t, after, len(before), "replace trim must not erase destination coverage of a restored identity")
		})
	}
}

func TestRecover_RelativeDatabasePath_PreservesMail(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	recoverRelativeMail(t, cwd)
}

// TestRecover_RelativeDatabasePath_SymlinkedCwd pins MTIX-254: when the tree is
// reached through a symlink, os.Getwd reports the symlink path, and a relative
// path computed from it lexically would not resolve from the physical cwd.
func TestRecover_RelativeDatabasePath_SymlinkedCwd(t *testing.T) {
	physical, err := os.Getwd()
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "tree-link")
	require.NoError(t, os.Symlink(physical, link))
	require.NoError(t, os.Chdir(link))
	t.Setenv("PWD", link)
	t.Cleanup(func() { require.NoError(t, os.Chdir(physical)) })
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, link, cwd, "setup: Getwd must report the symlink path")
	recoverRelativeMail(t, cwd)
}

// recoverRelativeMail recovers a three-mail board through a database path
// relative to cwd, the process working directory as Getwd reports it.
func recoverRelativeMail(t *testing.T, cwd string) {
	t.Helper()
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	var dbPath string
	require.NoError(t, raw.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&dbPath))
	// Relative paths resolve from the physical cwd, so compute them between
	// resolved paths (cwd may be a symlink path from $PWD).
	physCwd, err := filepath.EvalSymlinks(cwd)
	require.NoError(t, err)
	physDB, err := filepath.EvalSymlinks(dbPath)
	require.NoError(t, err)
	relative, err := filepath.Rel(physCwd, physDB)
	require.NoError(t, err)
	recovered, err := sqlite.Recover(t.Context(), relative, filepath.Join(t.TempDir(), "missing.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.Equal(t, 3, recovered.Export.NodeCount, "the existing board salvage accepts this relative path")
	require.NotNil(t, recovered.Export.InboxJournal, "readable relative database path must retain its journal: %v", recovered.Notes)
	require.Len(t, recovered.Export.InboxJournal.Events, 3)
}
