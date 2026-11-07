// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-112.17.16.16 round 3: a hub-known task keeps its uid (D),
// events a peer cannot apply are not journaled (E), a repeat emits nothing
// (A), an unfilled journal is left alone, and every column the merge writes
// is journaled or named (B).
package sqlite_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func fieldsNamed(res *sqlite.ImportResult, id string) []string {
	for _, c := range res.JournalLocalOnly {
		if c.ID == id {
			return c.Fields
		}
	}
	return nil
}

func retitleStore(t *testing.T, s *sqlite.Store, id, title string) {
	t.Helper()
	require.NoError(t, s.UpdateNode(context.Background(), id, &store.NodeUpdate{Title: &title}))
}

// TestImportMerge_HubKnownTask_KeepsItsUID_LaterEditsApplyBothWays: the file
// holds the task under another uid; the merge ignores it for a task the hub
// holds, names it local only, and an edit after the import applies on the
// peer, and a peer edit applies on the importer.
func TestImportMerge_HubKnownTask_KeepsItsUID_LaterEditsApplyBothWays(t *testing.T) {
	importer, peer, teammate := newTestStore(t), newTestStore(t), newTestStore(t)
	known := createSameIDTask(t, importer, "REC-1", "", 1, backfilledUID(t), "Shared task")
	replicate(t, importer, peer)
	markJournalPushed(t, importer)
	createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Imported title")

	res := mergeTheirs(t, importer, exportOf(t, teammate))

	var uid string
	require.NoError(t, importer.WriteDB().QueryRow(`SELECT uid FROM nodes WHERE id = 'REC-1'`).Scan(&uid))
	require.Equal(t, known, uid, "the hub knows the task by this uid: it is not replaced")
	require.Contains(t, fieldsNamed(res, "REC-1"), "uid")
	replicate(t, importer, peer)
	require.Equal(t, "Imported title", titleOf(t, peer, "REC-1"))
	retitleStore(t, importer, "REC-1", "Later importer edit")
	replicate(t, importer, peer)
	require.Equal(t, "Later importer edit", titleOf(t, peer, "REC-1"), "a later edit applies on the peer")
	retitleStore(t, peer, "REC-1", "Peer edit")
	replicate(t, peer, importer)
	require.Equal(t, "Peer edit", titleOf(t, importer, "REC-1"), "a peer edit applies on the importer")
}

// TestImportMerge_PendingTask_StillAdoptsTheFileUID: a task the hub does not
// hold yet takes the file's uid, with its pending events.
func TestImportMerge_PendingTask_StillAdoptsTheFileUID(t *testing.T) {
	importer, teammate := newTestStore(t), newTestStore(t)
	createSameIDTask(t, importer, "REC-1", "", 1, backfilledUID(t), "Shared task")
	markJournalPushed(t, importer)
	_, err := importer.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pending'`) // never pushed
	require.NoError(t, err)
	theirs := createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
	_, err = importer.WriteDB().Exec(`INSERT OR IGNORE INTO meta (key, value) VALUES ('sync.last_pull', 'x')`)
	require.NoError(t, err)

	res := mergeTheirs(t, importer, exportOf(t, teammate))

	var uid string
	require.NoError(t, importer.WriteDB().QueryRow(`SELECT uid FROM nodes WHERE id = 'REC-1'`).Scan(&uid))
	require.Equal(t, theirs, uid)
	require.NotContains(t, fieldsNamed(res, "REC-1"), "uid")
}

// TestImportMerge_DeletedTaskWithComment_CommentComesBeforeTheDelete: a peer
// applies a comment to a live task only, so the delete is journaled last.
func TestImportMerge_DeletedTaskWithComment_CommentComesBeforeTheDelete(t *testing.T) {
	importer, peer := peerPair(t, 1)
	markJournalPushed(t, importer)
	data := exportOf(t, importer)
	data.Nodes[0].DeletedAt = "2026-02-03T04:05:06Z"
	data.Nodes[0].Annotations = []model.Annotation{{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Author: "bob", Text: "last words", CreatedAt: sameIDTime}}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeTheirs(t, importer, data)

	var firstComment, firstDelete int
	require.NoError(t, importer.WriteDB().QueryRow(`SELECT COALESCE(MIN(rowid),0) FROM sync_events WHERE op_type = 'comment' AND sync_status = 'pending'`).Scan(&firstComment))
	require.NoError(t, importer.WriteDB().QueryRow(`SELECT COALESCE(MIN(rowid),0) FROM sync_events WHERE op_type = 'delete' AND sync_status = 'pending'`).Scan(&firstDelete))
	require.Positive(t, firstComment)
	require.Greater(t, firstDelete, firstComment, "the delete follows the comment")
	replicate(t, importer, peer)
	var raw string
	require.NoError(t, peer.WriteDB().QueryRow(`SELECT annotations FROM nodes WHERE id = 'REC-1'`).Scan(&raw))
	require.Contains(t, raw, "last words", "the peer applied the comment")
	require.NotEmpty(t, viewOf(t, peer, "REC-1").DeletedAt)
}

// TestImportMerge_RestoredTask_JournalsNothingAndNamesTheValues: no op
// restores a deleted task, so a peer could apply none of its field events;
// the merge journals nothing for it and names what changed.
func TestImportMerge_RestoredTask_JournalsNothingAndNamesTheValues(t *testing.T) {
	importer, _ := peerPair(t, 1)
	require.NoError(t, importer.DeleteNode(context.Background(), "REC-1", false, "alice"))
	markJournalPushed(t, importer)
	data := retitled(t, importer, "REC-1", "Back again")
	data.Nodes[0].DeletedAt, data.Nodes[0].DeletedBy = "", ""
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	require.Zero(t, countJournal(t, importer, `sync_status = 'pending'`))
	named := fieldsNamed(res, "REC-1")
	require.Contains(t, named, "deleted_at")
	require.Contains(t, named, "title")
}

func inProgressPair(t *testing.T) (importer, peer *sqlite.Store) {
	t.Helper()
	importer, peer = peerPair(t, 2)
	for _, s := range []*sqlite.Store{importer, peer} {
		_, err := s.WriteDB().Exec(`UPDATE nodes SET status = 'in_progress' WHERE id = 'REC-1'`)
		require.NoError(t, err)
	}
	return importer, peer
}

// TestImportMerge_BlocksDependencyRepeatedWithTheirs_EmitsNothingAndStaysBlocked:
// the repeat finds the dependency held; the file's status must not unblock
// the task, and nothing is journaled, so every replica agrees.
func TestImportMerge_BlocksDependencyRepeatedWithTheirs_EmitsNothingAndStaysBlocked(t *testing.T) {
	importer, peer := inProgressPair(t)
	markJournalPushed(t, importer)
	data := exportOf(t, importer)
	data.Dependencies = append(data.Dependencies, struct {
		FromID    string `json:"from_id"`
		ToID      string `json:"to_id"`
		DepType   string `json:"dep_type"`
		CreatedAt string `json:"created_at"`
	}{"REC-2", "REC-1", "blocks", sameIDTime.Format(time.RFC3339)})
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeTheirs(t, importer, data)
	replicate(t, importer, peer)
	mergeTheirs(t, importer, data)

	require.Zero(t, countJournal(t, importer, `sync_status = 'pending'`), "the repeat emits nothing")
	require.Equal(t, "blocked", viewOf(t, importer, "REC-1").Status)
	require.Equal(t, viewOf(t, importer, "REC-1"), viewOf(t, peer, "REC-1"))
}

// TestImportMerge_EmptyJournalBesideTasks_JournalsNothingAndSaysBackfill: a
// store filled before any sync has no create events; journaling only its
// changes would send orphans and make backfill refuse, so the merge leaves
// the journal empty and says so.
func TestImportMerge_EmptyJournalBesideTasks_JournalsNothingAndSaysBackfill(t *testing.T) {
	s := newTestStore(t)
	createPlainTask(t, s, "REC-1", 1, "", sameIDTime)
	_, err := s.WriteDB().Exec(`DELETE FROM sync_events`)
	require.NoError(t, err)

	_, res, err := s.ImportReconcile(context.Background(), retitledFile(t, s, "Imported"),
		sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, AssumeSynced: true})

	require.NoError(t, err)
	require.True(t, res.NeedsBackfill)
	require.Zero(t, countJournal(t, s, `1 = 1`))
	require.Equal(t, "Imported", titleOf(t, s, "REC-1"))
}

// TestImportMerge_EmptyStoreEmptyJournal_StillJournals: a store with no task
// has nothing a backfill would send, so the merge journals its creates.
func TestImportMerge_EmptyStoreEmptyJournal_StillJournals(t *testing.T) {
	source, s := newTestStore(t), newTestStore(t)
	createPlainTask(t, source, "REC-1", 1, "", sameIDTime)

	_, res, err := s.ImportReconcile(context.Background(), exportOf(t, source),
		sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, AssumeSynced: true})

	require.NoError(t, err)
	require.False(t, res.NeedsBackfill)
	require.Positive(t, countJournal(t, s, `op_type = 'create_node'`))
}

// TestMergeColumnTable_CoversEveryColumnOfTheSchema: a column the schema
// gains, or an annotation field, must be added to the table as journaled or
// local only before this test passes.
func TestMergeColumnTable_CoversEveryColumnOfTheSchema(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.WriteDB().Query(`SELECT name FROM pragma_table_info('nodes')`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	covered := sqlite.MergeColumnCoverage()
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		_, ok := covered[name]
		require.True(t, ok, "nodes column %s is neither journaled nor named local only", name)
	}
	require.NoError(t, rows.Err())
	typ := reflect.TypeOf(model.Annotation{})
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "-" || tag == "" {
			continue // not written to a file or a column
		}
		_, ok := covered["annotations."+tag]
		require.True(t, ok, "annotation field %s is neither journaled nor named local only", tag)
	}
}

// TestImportMerge_WrittenColumnsAndTimes_AreNamedLocalOnly: previous_status,
// deleted_by, activity, the times and a new annotation's resolved state are
// written by the merge and carried by no op, so each is named.
func TestImportMerge_WrittenColumnsAndTimes_AreNamedLocalOnly(t *testing.T) {
	importer, _ := peerPair(t, 1)
	markJournalPushed(t, importer)
	data := retitled(t, importer, "REC-1", "T1")
	n := &data.Nodes[0]
	n.PreviousStatus, n.DeletedBy, n.ClosedAt = "in_progress", "carol", "2026-01-02T00:00:00Z"
	n.CreatedAt, n.UpdatedAt = "2025-01-01T00:00:00Z", "2026-05-06T07:08:09Z"
	n.Activity = []model.ActivityEntry{{ID: "act-1", Type: model.ActivityTypeComment, Author: "carol", Text: "x"}}
	n.Annotations = []model.Annotation{{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Author: "bob", Text: "note", CreatedAt: sameIDTime, Resolved: true}}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	named := fieldsNamed(res, "REC-1")
	for _, f := range []string{"previous_status", "deleted_by", "closed_at", "created_at", "updated_at", "activity",
		"annotations.resolved", "annotations.created_at"} {
		require.Contains(t, named, f)
	}
}

// TestImportMerge_LocalRenumberOnASyncedStore_IsNamedLocalOnly: no event
// carries a renumber, so the hub keeps the old number.
func TestImportMerge_LocalRenumberOnASyncedStore_IsNamedLocalOnly(t *testing.T) {
	local, teammate := newTestStore(t), newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local task")
	markJournalPushed(t, local)
	createSameIDTask(t, teammate, "REC-1", "", 1, taskUID(t), "Teammate task")

	_, res, err := local.ImportReconcile(context.Background(), exportOf(t, teammate),
		sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: true})

	require.NoError(t, err)
	require.Contains(t, fieldsNamed(res, "REC-2"), "id (was REC-1)")
}
