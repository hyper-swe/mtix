// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// Survivors of the round 2 mutation run (MTIX-112.17.16.16, ruling 9): each
// test fails when the guarded condition is removed or inverted.

const survivorAnnID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func withAnnotation(t *testing.T, s *sqlite.Store, a model.Annotation) *sqlite.ExportData {
	t.Helper()
	data := exportOf(t, s)
	data.Nodes[0].Annotations = []model.Annotation{a}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	return data
}

// An event with the comment's id exists in the log: the hub has the comment,
// so no second event is journaled for it.
func TestImportMerge_CommentWhoseEventIDIsInTheLog_JournalsNoSecondEvent(t *testing.T) {
	s := syncedStoreWithTask(t)
	queueEvent(t, s, survivorAnnID, "REC-1", "", model.OpComment, "pushed")
	data := withAnnotation(t, s, model.Annotation{ID: survivorAnnID, Author: "bob", Text: "note", CreatedAt: sameIDTime})

	mergeTheirs(t, s, data)

	require.Zero(t, countJournal(t, s, `op_type = 'comment' AND sync_status = 'pending'`))
}

// A comment whose id is not in the log is journaled under that id.
func TestImportMerge_NewComment_JournaledUnderItsOwnID(t *testing.T) {
	s := syncedStoreWithTask(t)
	data := withAnnotation(t, s, model.Annotation{ID: survivorAnnID, Author: "bob", Text: "note", CreatedAt: sameIDTime})

	mergeTheirs(t, s, data)

	require.Equal(t, 1, countJournal(t, s, `op_type = 'comment' AND event_id = ? AND sync_status = 'pending'`, survivorAnnID))
}

// A blank annotation is stored and named, and no comment event is journaled.
func TestImportMerge_BlankAnnotation_JournalsNoCommentAndIsNamed(t *testing.T) {
	s := syncedStoreWithTask(t)
	data := withAnnotation(t, s, model.Annotation{ID: survivorAnnID, Author: "bob", Text: "", CreatedAt: sameIDTime})

	res := mergeTheirs(t, s, data)

	require.Zero(t, countJournal(t, s, `op_type = 'comment'`))
	require.Contains(t, fieldsNamed(res, "REC-1"), "annotations")
}

// A held annotation whose resolved state changes is named, not journaled.
func TestImportMerge_HeldAnnotationResolved_IsNamedAndJournalsNothing(t *testing.T) {
	s := syncedStoreWithTask(t)
	_, err := s.WriteDB().Exec(`UPDATE nodes SET annotations = ? WHERE id = 'REC-1'`,
		`[{"id":"`+survivorAnnID+`","author":"bob","text":"note","created_at":"2026-09-24T08:00:00Z","resolved":false}]`)
	require.NoError(t, err)
	markJournalPushed(t, s)
	data := withAnnotation(t, s, model.Annotation{ID: survivorAnnID, Author: "bob", Text: "note", CreatedAt: sameIDTime, Resolved: true})

	res := mergeTheirs(t, s, data)

	require.Contains(t, fieldsNamed(res, "REC-1"), "annotations.resolved")
	require.Zero(t, countJournal(t, s, `op_type = 'comment' AND sync_status = 'pending'`))
}

// A created task whose uid is already an event id in the log is written and
// named unsent with no create journaled, so nothing collides with that event.
func TestImportMerge_CreatedTaskWhoseUIDIsAnEventID_JournalsNoCreateAndKeepsTheLog(t *testing.T) {
	s := syncedStoreWithTask(t)
	other := newTestStore(t)
	uid := clock.MustNewEventID()
	createPlainTask(t, other, "REC-1", 1, "", sameIDTime)
	createPlainTask(t, other, "REC-2", 2, uid, sameIDTime)
	queueEvent(t, s, uid, "REC-1", "", model.OpComment, "pushed")
	events := countJournal(t, s, `1 = 1`)

	mergeFile(t, s, exportOf(t, other))

	require.Zero(t, countJournal(t, s, `op_type = 'create_node' AND node_id = 'REC-2'`))
	require.Equal(t, events, countJournal(t, s, `1 = 1`), "the log is untouched")
	require.Equal(t, "Shared task", titleOf(t, s, "REC-2"))
}

// A pushed create matched by event id alone (no uid column) makes the hub
// know the task by that uid.
func TestImportMerge_HubKnownByEventIDOnly_KeepsTheLocalUID(t *testing.T) {
	importer, teammate := newTestStore(t), newTestStore(t)
	mine := createSameIDTask(t, importer, "REC-1", "", 1, backfilledUID(t), "Shared task")
	_, err := importer.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed', uid = NULL, event_id = ? WHERE op_type = 'create_node'`, mine)
	require.NoError(t, err)
	createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")

	mergeTheirs(t, importer, exportOf(t, teammate))

	var uid string
	require.NoError(t, importer.WriteDB().QueryRow(`SELECT uid FROM nodes WHERE id = 'REC-1'`).Scan(&uid))
	require.Equal(t, mine, uid)
}

func progressFile(t *testing.T, s *sqlite.Store, id string, progress float64) *sqlite.ExportData {
	t.Helper()
	data := exportOf(t, s)
	for i := range data.Nodes {
		if data.Nodes[i].ID == id {
			data.Nodes[i].Progress = progress
			data.Nodes[i].Title = "Retitled " + id
			data.Nodes[i].ContentHash = plainHash(data.Nodes[i].Title)
		}
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	return data
}

// A leaf's progress changed without a status change is named; a parent's
// follows its children and is not.
func TestImportMerge_ProgressChange_NamedForALeafAndNotForAParent(t *testing.T) {
	s := syncedStoreWithTask(t)
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID: "REC-1.1", ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1, Title: "Child", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeForDepth(1),
		ContentHash: plainHash("Child"), CreatedAt: sameIDTime, UpdatedAt: sameIDTime}))
	markJournalPushed(t, s)

	leaf := mergeTheirs(t, s, progressFile(t, s, "REC-1.1", 0.5))
	parent := mergeTheirs(t, s, progressFile(t, s, "REC-1", 0.7))

	require.Contains(t, fieldsNamed(leaf, "REC-1.1"), "progress")
	require.NotContains(t, fieldsNamed(parent, "REC-1"), "progress")
}

// A closed time that changes with the status stays named: a peer stamps its
// own event time.
func TestImportMerge_ClosedAtWithAStatusChange_IsStillNamed(t *testing.T) {
	importer, peer := peerPair(t, 1)
	markJournalPushed(t, importer)
	data := exportOf(t, importer)
	data.Nodes[0].Status, data.Nodes[0].ClosedAt, data.Nodes[0].Progress = "done", "2026-01-02T00:00:00Z", 1
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)
	replicate(t, importer, peer)

	require.Contains(t, fieldsNamed(res, "REC-1"), "closed_at")
	require.Equal(t, "done", viewOf(t, peer, "REC-1").Status)
}

// The settle position follows a task by its uid when its number changed.
func TestLocalOnlyImports_EventUnderTheOldNumberSettlesByUID(t *testing.T) {
	s := newTestStore(t)
	uid := backfilledUID(t)
	createSameIDTask(t, s, "REC-1", "", 1, uid, "Shared task")
	markJournalPushed(t, s)
	require.NoError(t, s.RecordLocalOnlyImports(context.Background(),
		[]sqlite.LocalOnlyImport{{ID: "REC-1", Fields: []string{"title"}}}))
	_, err := s.WriteDB().Exec(`UPDATE nodes SET id = 'REC-9', seq = 9 WHERE id = 'REC-1'`)
	require.NoError(t, err)
	require.NoError(t, s.RecordLocalOnlyImports(context.Background(),
		[]sqlite.LocalOnlyImport{{ID: "REC-9", Fields: []string{"title"}}}))
	ev := fieldEvent(t, "REC-1", "title", "Hub title", 9)
	ev.UID = uid
	queueEvent(t, s, ev.EventID, "REC-1", uid, model.OpUpdateField, "applied")
	_, err = s.WriteDB().Exec(`UPDATE sync_events SET payload = ? WHERE event_id = ?`, string(ev.Payload), ev.EventID)
	require.NoError(t, err)

	live, _ := liveOnly(t, s)

	require.Empty(t, live, "the event names the old number but the task's uid")
}
