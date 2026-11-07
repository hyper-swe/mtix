// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-112.17.16.16 round 4, principle F: a column is journaled
// only when a peer is shown to apply the op; a task whose creation the sync
// log lacks gets no event (and nothing under it); a store that syncs keeps
// every uid; a deleted task stays deleted; one rule blocks and reblocks.
package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// addChildOf appends a new task child (REC-<parent>.<seq>) under parent to the
// file.
func addChildOf(t *testing.T, data *sqlite.ExportData, parent, id string) {
	t.Helper()
	idx := -1
	for i := range data.Nodes {
		if data.Nodes[i].ID == parent {
			idx = i
		}
	}
	require.GreaterOrEqual(t, idx, 0)
	base := data.Nodes[idx]
	child := base
	child.ID, child.ParentID, child.Depth, child.Seq = id, parent, base.Depth+1, 1
	child.UID, child.Title = taskUID(t)+"-child", "Child "+id
	child.NodeType = string(model.NodeTypeForDepth(child.Depth))
	child.ContentHash = plainHash(child.Title)
	child.Annotations, child.Activity = nil, nil
	data.Nodes = append(data.Nodes, child)
	data.NodeCount = len(data.Nodes)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
}

// addHeldChild creates child id under parent in s, as a local create the sync
// log records, so the merge treats it as a held task.
func addHeldChild(t *testing.T, s *sqlite.Store, parent, id string) {
	t.Helper()
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID: id, ParentID: parent, Project: "REC", Depth: 1, Seq: 1, Title: "Child " + id, Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeForDepth(1),
		ContentHash: plainHash("Child " + id), CreatedAt: sameIDTime, UpdatedAt: sameIDTime}))
}

// halfFilledPair returns an importer holding REC-1 (created in the log and
// sent) and REC-2 (created before any sync: no create row), and a peer that
// holds REC-1 only.
func halfFilledPair(t *testing.T) (importer, peer *sqlite.Store) {
	t.Helper()
	importer, peer = newTestStore(t), newTestStore(t)
	createPlainTask(t, importer, "REC-1", 1, "", sameIDTime)
	replicate(t, importer, peer)
	createPlainTask(t, importer, "REC-2", 2, "", sameIDTime)
	_, err := importer.WriteDB().Exec(`DELETE FROM sync_events WHERE node_id = 'REC-2'`)
	require.NoError(t, err)
	return importer, peer
}

// TestImportMerge_TaskWithoutCreateRow_AndItsNewChild_GetNoEvents: a store
// with one journal row holds a task the log never created; a merge that
// changes it and adds a child under it journals nothing for either, names
// both, says backfill sends them, and still journals the task the log holds.
func TestImportMerge_TaskWithoutCreateRow_AndItsNewChild_GetNoEvents(t *testing.T) {
	importer, peer := halfFilledPair(t)
	data := exportOf(t, importer)
	for i := range data.Nodes {
		data.Nodes[i].Title = "Retitled " + data.Nodes[i].ID
		data.Nodes[i].ContentHash = plainHash(data.Nodes[i].Title)
	}
	addChildOf(t, data, "REC-2", "REC-2.1")

	res := mergeTheirs(t, importer, data)

	require.Zero(t, countJournal(t, importer, `sync_status = 'pending' AND node_id IN ('REC-2', 'REC-2.1')`),
		"no event for a task the log never created, or for its child")
	require.ElementsMatch(t, []string{"REC-2", "REC-2.1"}, res.UnsentTasks)
	require.True(t, res.NeedsBackfill)
	replicate(t, importer, peer)
	require.Equal(t, "Retitled REC-1", titleOf(t, peer, "REC-1"), "the task the log holds still journals")
}

// TestBackfill_NonEmptyJournal_SendsOnlyTheTasksTheLogLacks: backfill works on
// a journal that holds some creates: it emits the missing tasks (the merged
// values) and nothing it already holds, so a peer ends with every task.
func TestBackfill_NonEmptyJournal_SendsOnlyTheTasksTheLogLacks(t *testing.T) {
	importer, peer := halfFilledPair(t)
	data := exportOf(t, importer)
	addChildOf(t, data, "REC-2", "REC-2.1")
	mergeTheirs(t, importer, data)
	before := countJournal(t, importer, `op_type = 'create_node' AND node_id = 'REC-1'`)

	_, err := importer.Backfill(context.Background(), false)

	require.NoError(t, err)
	require.Equal(t, before, countJournal(t, importer, `op_type = 'create_node' AND node_id = 'REC-1'`), "no second create for REC-1")
	replicate(t, importer, peer)
	require.Equal(t, "Child REC-2.1", titleOf(t, peer, "REC-2.1"))
	require.Equal(t, "Shared task", titleOf(t, peer, "REC-2"))
}

// TestImportMerge_SyncedStoreTaskWithoutCreateRow_KeepsItsUID: principle D on
// every transport: a store that syncs never adopts another uid for a task it
// holds, whatever its journal says (a relay snapshot clone has no create row).
func TestImportMerge_SyncedStoreTaskWithoutCreateRow_KeepsItsUID(t *testing.T) {
	importer, teammate := newTestStore(t), newTestStore(t)
	known := createSameIDTask(t, importer, "REC-1", "", 1, backfilledUID(t), "Shared task")
	markJournalPushed(t, importer)
	_, err := importer.WriteDB().Exec(`DELETE FROM sync_events WHERE node_id = 'REC-1'`)
	require.NoError(t, err)
	_, err = importer.MarkHubConnected(context.Background(), "config") // hub evidence outlives the journal rows
	require.NoError(t, err)
	createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")

	res := mergeTheirs(t, importer, exportOf(t, teammate))

	var uid string
	require.NoError(t, importer.WriteDB().QueryRow(`SELECT uid FROM nodes WHERE id = 'REC-1'`).Scan(&uid))
	require.Equal(t, known, uid)
	require.Contains(t, fieldsNamed(res, "REC-1"), "uid")
}

// TestImportMerge_DeletedTask_StaysDeletedAndTheFileChangesAreNotApplied: no
// op restores a deleted task, so on a store that syncs the merge leaves it
// deleted, applies none of the file's changes to it, and names it.
func TestImportMerge_DeletedTask_StaysDeletedAndTheFileChangesAreNotApplied(t *testing.T) {
	importer, peer := peerPair(t, 1)
	require.NoError(t, importer.DeleteNode(context.Background(), "REC-1", false, "alice"))
	replicate(t, importer, peer)
	data := retitled(t, importer, "REC-1", "Back again")
	data.Nodes[0].DeletedAt, data.Nodes[0].DeletedBy = "", ""
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	require.Equal(t, []string{"REC-1"}, res.SkippedDeleted)
	require.NotEmpty(t, viewOf(t, importer, "REC-1").DeletedAt, "still deleted")
	require.NotEqual(t, "Back again", viewOf(t, importer, "REC-1").Title)
	require.Zero(t, countJournal(t, importer, `sync_status = 'pending'`))
	require.Equal(t, viewOf(t, importer, "REC-1"), viewOf(t, peer, "REC-1"))
}

// TestImportMerge_RepeatWithADeadBlocker_EmitsNothingAndStaysBlocked: block and
// reblock are one rule, the one a peer uses (any blocks dependency blocks an
// open or in-progress task, whatever state the blocker is in), so a repeat
// with a done, deleted or invalidated blocker emits nothing.
func TestImportMerge_RepeatWithADeadBlocker_EmitsNothingAndStaysBlocked(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"done", `UPDATE nodes SET status = 'done' WHERE id = 'REC-2'`},
		{"deleted", `UPDATE nodes SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = 'REC-2'`},
		{"invalidated", `UPDATE nodes SET status = 'invalidated' WHERE id = 'REC-2'`},
		{"cancelled", `UPDATE nodes SET status = 'cancelled' WHERE id = 'REC-2'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			importer, peer := inProgressPair(t)
			markJournalPushed(t, importer)
			_, err := importer.WriteDB().Exec(tc.sql)
			require.NoError(t, err)
			_, err = peer.WriteDB().Exec(tc.sql)
			require.NoError(t, err)
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
		})
	}
}

// TestImportMerge_NoOpMergeOverTheCap_IsNotRefused: a merge that changes
// nothing journals zero events, which never pass the cap.
func TestImportMerge_NoOpMergeOverTheCap_IsNotRefused(t *testing.T) {
	importer, _ := peerPair(t, 1)
	_, err := importer.WriteDB().Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('sync.max_queue_size', '1')`)
	require.NoError(t, err)
	require.Greater(t, countJournal(t, importer, `sync_status = 'pending' OR sync_status = 'pushed'`), 0)
	_, err = importer.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pending'`)
	require.NoError(t, err)

	_, _, err = importer.ImportReconcile(context.Background(), exportOf(t, importer), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Workflow: sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs}})

	require.NoError(t, err)
}

// TestImportMerge_SameAnnotationIDOnTwoTasks_JournalsTwoEventsUnderDistinctIDs:
// the second comment takes a fresh event id instead of failing the import.
func TestImportMerge_SameAnnotationIDOnTwoTasks_JournalsTwoEventsUnderDistinctIDs(t *testing.T) {
	importer, peer := peerPair(t, 2)
	data := exportOf(t, importer)
	for i := range data.Nodes {
		data.Nodes[i].Annotations = []model.Annotation{{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Author: "bob", Text: "same id", CreatedAt: sameIDTime}}
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeTheirs(t, importer, data)

	require.Equal(t, 2, countJournal(t, importer, `op_type = 'comment' AND sync_status = 'pending'`))
	replicate(t, importer, peer)
}

// TestImportMerge_LabelsClearedToEmpty_JournalAnEmptyList: the event carries
// an empty list, never the text null.
func TestImportMerge_LabelsClearedToEmpty_JournalAnEmptyList(t *testing.T) {
	importer, peer := peerPair(t, 1)
	_, err := importer.WriteDB().Exec(`UPDATE nodes SET labels = '["a"]' WHERE id = 'REC-1'`)
	require.NoError(t, err)
	_, err = peer.WriteDB().Exec(`UPDATE nodes SET labels = '["a"]' WHERE id = 'REC-1'`)
	require.NoError(t, err)
	data := exportOf(t, importer)
	data.Nodes[0].Labels = ""
	data.Nodes[0].ContentHash = plainHash(data.Nodes[0].Title)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeTheirs(t, importer, data)
	replicate(t, importer, peer)

	require.Equal(t, viewOf(t, importer, "REC-1").Labels, viewOf(t, peer, "REC-1").Labels)
	require.NotEqual(t, "null", viewOf(t, peer, "REC-1").Labels)
}

func columnOf(t *testing.T, s *sqlite.Store, col string) string {
	t.Helper()
	var v sql.NullString
	require.NoError(t, s.WriteDB().QueryRow(fmt.Sprintf(`SELECT CAST(%s AS TEXT) FROM nodes WHERE id = 'REC-1'`, col)).Scan(&v))
	return v.String
}
