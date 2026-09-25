// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// End-to-end-style tests for MTIX-95.31.9 (FR-15.2i, FR-7.8): a pulled
// board without uids (written by a client at 0.3 or older, schema 1.0.0)
// that holds a task with another title under a local id is never merged
// silently. The automatic import refuses and names the pair ("a task under
// this id with a different title and no uid to compare"); the merge it
// recommends renumbers the local task only with --confirm, keeps it whole
// and names the pair in its report. Written red-first against the
// MTIX-95.31.6 code, which listed only "field uid" and whose merge replaced
// the local title and description.
package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// withoutUIDs turns a board into one a client at 0.3 or older writes:
// schema 1.0.0, no annotations or activity (asOlderClientBoard), and no
// uids.
func withoutUIDs(t *testing.T, board []byte) []byte {
	t.Helper()
	data, err := sqlite.DecodeExportData(bytes.NewReader(asOlderClientBoard(t, board)))
	require.NoError(t, err)
	for i := range data.Nodes {
		data.Nodes[i].UID = ""
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	out, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	return out
}

// TestAutoImport_UIDlessBoardWithAnotherTitle_RefusedAndMergeRenumbers
// verifies the automatic import of a board without uids whose PROJ-2 is
// another task (another title) refuses and names the pair, and not PROJ-1,
// whose title is the same; the recommended merge writes nothing without
// --confirm, reports the renumbering and the pair, and with --confirm the
// board's task takes PROJ-2 while the local PROJ-2 moves to PROJ-3 whole,
// with its uid and description.
func TestAutoImport_UIDlessBoardWithAnotherTitle_RefusedAndMergeRenumbers(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	mine, err := f.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	f.pull(t, withoutUIDs(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		n := &d.Nodes[nodeIndex(t, d, "PROJ-2")]
		n.Title, n.Description, n.ContentHash = "Teammate's own task", "Why: theirs", "h-theirs"
	})))
	before := f.storeSnapshot(t)

	require.ErrorIs(t, f.svc.AutoImport(ctx, f.mtixDir), service.ErrAutoImportRefused)
	assert.Equal(t, before, f.storeSnapshot(t))
	msg := f.notices.String()
	assert.Contains(t, msg, `  PROJ-2: a task under this id with a different title and no uid to compare `+
		`(local "Task PROJ-2", file "Teammate's own task")`)
	assert.Contains(t, msg, "a local task listed with a different title and no uid to compare is taken for a "+
		"different task")
	assert.Contains(t, msg, "the renumbered one holds your comments, activity and field values the board lacked: "+
		"move them to the task at the id, or keep the renumbered copy, before you delete anything")
	assert.NotContains(t, msg, "PROJ-1: a task under this id")

	report, _, err := f.store.ImportReconcile(ctx, f.pulledBoard(t), sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
	assert.Equal(t, before, f.storeSnapshot(t), "merge without --confirm writes nothing")
	assert.Equal(t, []sqlite.ImportRemapEntry{{UID: mine.UID, OldPath: "PROJ-2", NewPath: "PROJ-3"}}, report.LocalRenumbers)
	assert.Equal(t, []sqlite.ImportTitleMismatch{{ID: "PROJ-2", LocalTitle: "Task PROJ-2",
		FileTitle: "Teammate's own task"}}, report.TitleMismatches)

	_, _, err = f.store.ImportReconcile(ctx, f.pulledBoard(t), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	theirs, err := f.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, "Teammate's own task", theirs.Title)
	kept, err := f.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, err)
	assert.Equal(t, mine.Title, kept.Title, "the local task is never overwritten")
	assert.Equal(t, mine.Description, kept.Description)
	assert.Equal(t, mine.UID, kept.UID)
	assert.Equal(t, 2, f.annotationCount(t, "PROJ-1"), "the merge keeps PROJ-1's annotations")
}

// TestAutoImport_UIDlessBoardWithTwoOtherTitles_MergeOptionNamesBoth
// verifies the merge option names every task the board without uids holds
// with another title, here PROJ-1 and PROJ-2 (MTIX-95.31.9).
func TestAutoImport_UIDlessBoardWithTwoOtherTitles_MergeOptionNamesBoth(t *testing.T) {
	f := newGuardFixture(t)
	f.pull(t, withoutUIDs(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		for _, id := range []string{"PROJ-1", "PROJ-2"} {
			d.Nodes[nodeIndex(t, d, id)].Title = "Teammate's " + id
		}
	})))

	require.ErrorIs(t, f.svc.AutoImport(context.Background(), f.mtixDir), service.ErrAutoImportRefused)
	assert.Contains(t, f.notices.String(), "no uid to compare is taken for a different task (here PROJ-1, PROJ-2)")
}

// boardUIDs returns the uid of every node on f's .mtix/tasks.json, by id.
func boardUIDs(t *testing.T, f *guardFixture) map[string]string {
	t.Helper()
	board := f.pulledBoard(t)
	uids := make(map[string]string, len(board.Nodes))
	for i := range board.Nodes {
		uids[board.Nodes[i].ID] = board.Nodes[i].UID
	}
	return uids
}

// TestAutoImport_FreshClonesOfUIDlessBoard_RetitleConvergesWithoutDuplicate
// is the MTIX-95.31.9 round-1 regression: two fresh clones import a board
// written by a client at 0.1.5 (no uids). Each gives every task its own
// marked backfill uid, so the boards they write carry a uid for every node.
// D retitles PROJ-1 and E pulls D's board: the only refusal is the one-time
// "treated as the same task (uid assigned at upgrade)", never "no uid to
// compare"; the merge it recommends needs no --confirm, adopts D's uids and
// duplicates nothing; and the next retitle imports without a refusal.
func TestAutoImport_FreshClonesOfUIDlessBoard_RetitleConvergesWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	board := withoutUIDs(t, newGuardFixture(t).teammateBoard(t, func(*sqlite.ExportData) {}))
	d, e := newProjectFixture(t), newProjectFixture(t)
	for _, x := range []*guardFixture{d, e} {
		x.pull(t, board)
		require.NoError(t, x.svc.AutoImport(ctx, x.mtixDir))
	}
	require.NotEqual(t, uidAt(t, d, "PROJ-1"), uidAt(t, e, "PROJ-1"), "each clone backfilled its own uid")
	retitle := func(t *testing.T, f *guardFixture, id, title string) {
		t.Helper()
		require.NoError(t, f.store.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title}))
		require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	}
	retitle(t, d, "PROJ-1", "Retitled by D")
	for id, uid := range boardUIDs(t, d) {
		assert.True(t, model.IsBackfillUID(uid), "the 2.0.0 board D writes carries a uid for %s", id)
	}

	shareBoard(t, d, e)
	require.ErrorIs(t, e.svc.AutoImport(ctx, e.mtixDir), service.ErrAutoImportRefused)
	msg := e.notices.String()
	assert.Contains(t, msg, `PROJ-1: treated as the same task (uid assigned at upgrade) (local "Task PROJ-1", `+
		`file "Retitled by D")`)
	assert.NotContains(t, msg, "no uid to compare")
	report, _, err := e.store.ImportReconcile(ctx, e.pulledBoard(t), sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.NoError(t, err, "no --confirm: nothing is renumbered")
	assert.Empty(t, report.LocalRenumbers)
	require.NoError(t, e.svc.ResolveRefusalByImport(e.mtixDir, filepath.Join(e.mtixDir, "tasks.json")))
	require.NoError(t, e.svc.AutoExport(ctx, e.mtixDir))
	assert.Equal(t, boardUIDs(t, d), boardUIDs(t, e), "the uids converged, and no task was duplicated")
	node, err := e.store.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "Retitled by D", node.Title)

	e.notices.Reset()
	retitle(t, d, "PROJ-2", "Retitled again by D")
	shareBoard(t, d, e)
	require.NoError(t, e.svc.AutoImport(ctx, e.mtixDir), "the next exchange imports cleanly")
	assert.NotContains(t, e.notices.String(), refusalHeader)
	assert.Equal(t, boardUIDs(t, d), boardUIDs(t, e))
	node, err = e.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, "Retitled again by D", node.Title)
}
