// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Round-2 tests for MTIX-95.31.4 (FR-15.2i): the merge a refusal
// recommends keeps a listed field value a stale board leaves empty; a task
// another clone renumbered follows the board to its new id without loss or
// confirmation; clones whose shared tasks carry different backfilled uids
// (upgraded from before uids were shared) import each other's boards
// cleanly; and mtix sync never reports a board as in sync while an import
// of it is pending or a task under one id differs. Written red-first
// against round 1.
package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// clones returns n clones of one board: each has imported origin's board.
func clones(t *testing.T, origin *guardFixture, n int) []*guardFixture {
	t.Helper()
	out := make([]*guardFixture, n)
	for i := range out {
		out[i] = teammateClone(t, origin)
	}
	return out
}

// uidAt returns the uid of node id in f's store.
func uidAt(t *testing.T, f *guardFixture, id string) string {
	t.Helper()
	node, err := f.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	return node.UID
}

// TestAutoImport_StaleBoardBlanksField_RecommendedMergeKeepsIt verifies
// the merge a refusal recommends keeps a field value the refusal lists: A
// sets PROJ-2's description, then pulls B's board, whose copy of PROJ-2 is
// older (and, from mtix 0.5.3, carries no activity). The refusal lists the
// description and says merge keeps it; the merge keeps it and adds B's
// task.
func TestAutoImport_StaleBoardBlanksField_RecommendedMergeKeepsIt(t *testing.T) {
	for _, older := range []bool{false, true} {
		name := "2.0.0 board"
		if older {
			name = "board written by mtix 0.5.3"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			pair := clones(t, newGuardFixture(t), 2)
			a, b := pair[0], pair[1]
			desc := "Local description"
			require.NoError(t, a.store.UpdateNode(ctx, "PROJ-2", &store.NodeUpdate{Description: &desc}))
			require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
			theirs := createLocalTask(t, b, "B's own task")
			require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
			board := b.read(t, "tasks.json")
			if older {
				board = asOlderClientBoard(t, board)
			}
			a.pull(t, board)

			require.ErrorIs(t, a.svc.AutoImport(ctx, a.mtixDir), service.ErrAutoImportRefused)
			msg := a.notices.String()
			assert.Contains(t, lineWith(msg, "  PROJ-2: "), "description")
			assert.Contains(t, msg, "keeps every value the refusal lists")

			_, _, err := a.store.ImportReconcile(ctx, a.pulledBoard(t), sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
			require.NoError(t, err)
			node, err := a.store.GetNode(ctx, "PROJ-2")
			require.NoError(t, err)
			assert.Equal(t, desc, node.Description, "the merge keeps the listed field value")
			assert.Equal(t, theirs.UID, uidAt(t, a, "PROJ-3"), "and adds the teammate's task")
		})
	}
}

// TestAutoImport_ThreeClones_RenumberedTaskFollowsWithoutLoss verifies a
// clone that holds a task another clone renumbered follows the board: A
// and B each create PROJ-3; C and D hold A's; A merges B's board, which
// moves A's task to PROJ-4. C's automatic import of A's board then applies
// with no refusal, and D's merge of it needs no confirmation.
func TestAutoImport_ThreeClones_RenumberedTaskFollowsWithoutLoss(t *testing.T) {
	ctx := context.Background()
	all := clones(t, newGuardFixture(t), 4)
	a, b, c, d := all[0], all[1], all[2], all[3]
	mine := createLocalTask(t, a, "A's own task")
	theirs := createLocalTask(t, b, "B's own task")
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
	for _, x := range []*guardFixture{c, d} {
		shareBoard(t, a, x)
		require.NoError(t, x.svc.AutoImport(ctx, x.mtixDir))
	}
	shareBoard(t, b, a)
	require.ErrorIs(t, a.svc.AutoImport(ctx, a.mtixDir), service.ErrAutoImportRefused)
	_, _, err := a.store.ImportReconcile(ctx, a.pulledBoard(t), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	require.NoError(t, a.svc.ResolveRefusalByImport(a.mtixDir, filepath.Join(a.mtixDir, "tasks.json")))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))

	shareBoard(t, a, c)
	require.NoError(t, c.svc.AutoImport(ctx, c.mtixDir), "C pulls A's board cleanly")
	assert.NotContains(t, c.notices.String(), refusalHeader)
	assert.Equal(t, theirs.UID, uidAt(t, c, "PROJ-3"))
	assert.Equal(t, mine.UID, uidAt(t, c, "PROJ-4"))
	assert.Equal(t, 1, c.annotationCount(t, "PROJ-4"), "the moved task keeps its annotation")

	shareBoard(t, a, d)
	report, _, err := d.store.ImportReconcile(ctx, d.pulledBoard(t), sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.NoError(t, err, "no confirmation: the task only follows the board")
	assert.Empty(t, report.LocalRenumbers)
	assert.Equal(t, []sqlite.ImportRemapEntry{{UID: mine.UID, OldPath: "PROJ-3", NewPath: "PROJ-4"}}, report.Moved)
	assert.Equal(t, theirs.UID, uidAt(t, d, "PROJ-3"))
	assert.Equal(t, mine.UID, uidAt(t, d, "PROJ-4"))
}

// backfillUIDs gives every node in f's store a uid of its own and exports,
// as a clone upgraded from before uids were shared (0.3.x) does: each clone
// minted local uids for the same shared tasks.
func backfillUIDs(t *testing.T, f *guardFixture) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"PROJ-1", "PROJ-2"} {
		uid, err := clock.NewEventID()
		require.NoError(t, err)
		_, err = f.store.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = ?`, uid, id)
		require.NoError(t, err)
	}
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
}

// TestAutoImport_UpgradedClonesWithBackfilledUIDs_ImportCleanly verifies
// clones whose shared tasks carry different backfilled uids treat them as
// the same tasks (same id and creation time): the automatic import of the
// other clone's board applies without a refusal and adopts its uids, and a
// merge of it renumbers nothing.
func TestAutoImport_UpgradedClonesWithBackfilledUIDs_ImportCleanly(t *testing.T) {
	ctx := context.Background()
	origin := newGuardFixture(t)
	all := clones(t, origin, 2)
	b, d := all[0], all[1]
	for _, x := range all {
		backfillUIDs(t, x)
	}
	require.NotEqual(t, uidAt(t, origin, "PROJ-1"), uidAt(t, b, "PROJ-1"))
	writeLocalTask(t, origin, "PROJ-3", 3)
	require.NoError(t, origin.svc.AutoExport(ctx, origin.mtixDir))

	shareBoard(t, origin, b)
	require.NoError(t, b.svc.AutoImport(ctx, b.mtixDir))
	assert.NotContains(t, b.notices.String(), refusalHeader)
	assert.Equal(t, uidAt(t, origin, "PROJ-1"), uidAt(t, b, "PROJ-1"), "the file's uid is adopted")

	shareBoard(t, origin, d)
	report, _, err := d.store.ImportReconcile(ctx, d.pulledBoard(t), sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.NoError(t, err)
	assert.Empty(t, report.LocalRenumbers)
	assert.Empty(t, report.Moved)
	assert.Equal(t, uidAt(t, origin, "PROJ-2"), uidAt(t, d, "PROJ-2"), "the file's uid is adopted")
}

// TestCompare_PendingImportOrOtherUID_NeverInSync verifies mtix sync never
// reports the board as in sync while an automatic import of it is pending,
// or while a task under one id carries another uid in the file.
func TestCompare_PendingImportOrOtherUID_NeverInSync(t *testing.T) {
	t.Run("pending refusal", func(t *testing.T) {
		f, _ := refusedFixture(t)
		report, err := f.svc.Compare(context.Background(), f.mtixDir)
		require.NoError(t, err)
		assert.Empty(t, report.OnlyInDB)
		assert.Empty(t, report.OnlyInFile)
		assert.False(t, report.InSync, "an import of tasks.json is pending")
	})
	t.Run("another uid under an id", func(t *testing.T) {
		f := newGuardFixture(t)
		board := f.teammateBoard(t, func(d *sqlite.ExportData) {
			d.Nodes[nodeIndex(t, d, "PROJ-2")].UID = "01a0d56f-0000-7000-8000-00000000e001"
		})
		f.pull(t, board)
		writeStoredHash(t, filepath.Dir(f.mtixDir), hashBytes(board)) // recorded as imported: nothing pending
		report, err := f.svc.Compare(context.Background(), f.mtixDir)
		require.NoError(t, err)
		assert.False(t, report.InSync)
		assert.Equal(t, []string{"PROJ-2"}, report.DifferentUID)
	})
	t.Run("in sync", func(t *testing.T) {
		f := newGuardFixture(t)
		report, err := f.svc.Compare(context.Background(), f.mtixDir)
		require.NoError(t, err)
		assert.True(t, report.InSync)
		_, statErr := os.Stat(filepath.Join(f.mtixDir, "tasks.json"))
		require.NoError(t, statErr)
	})
}
