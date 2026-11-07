// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// pendingPayloads returns the payloads of the pending events of op for node id.
func pendingPayloads(t *testing.T, s *sqlite.Store, id, op string) []string {
	t.Helper()
	rows, err := s.WriteDB().Query(`SELECT payload FROM sync_events WHERE node_id = ? AND op_type = ? AND sync_status = 'pending' ORDER BY rowid`, id, op)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []string
	for rows.Next() {
		var p string
		require.NoError(t, rows.Scan(&p))
		out = append(out, p)
	}
	return out
}

// TestImportMerge_EachChangedField_IsJournaledWithItsOpAndValue: one changed
// field per case, a title change beside it so the file's values are written;
// the journal holds exactly that field's event with the stored value, and a
// peer converges. It kills a mutant that drops or swaps a field's event.
func TestImportMerge_EachChangedField_IsJournaledWithItsOpAndValue(t *testing.T) {
	tests := []struct {
		name  string
		op    string
		field string
		want  string
	}{
		{name: "labels", op: "update_field", field: "labels", want: `["a","b"]`},
		{name: "assignee", op: "update_field", field: "assignee", want: `"alice"`},
		{name: "priority", op: "update_field", field: "priority", want: `1`},
		{name: "issue_type", op: "update_field", field: "issue_type", want: `"bug"`},
		{name: "agent_state", op: "update_field", field: "agent_state", want: `"working"`},
		{name: "description", op: "update_field", field: "description", want: `"D"`},
		{name: "prompt", op: "set_prompt", want: `"prompt_text":"P"`},
		{name: "acceptance", op: "set_acceptance", want: `"acceptance_text":"A"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			importer, peer := peerPair(t, 1)
			markJournalPushed(t, importer)
			data := exportOf(t, importer)
			n := &data.Nodes[0]
			n.Title = "Changed"
			switch tt.name {
			case "labels":
				n.Labels = `["a","b"]`
			case "assignee":
				n.Assignee = "alice"
			case "priority":
				n.Priority = 1
			case "issue_type":
				n.IssueType = "bug"
			case "agent_state":
				n.AgentState = "working"
			case "description":
				n.Description = "D"
			case "prompt":
				n.Prompt = "P"
			case "acceptance":
				n.Acceptance = "A"
			}
			var labels []string
			if tt.name == "labels" {
				labels = []string{"a", "b"}
			}
			n.ContentHash = model.ComputeContentHash(n.Title, n.Description, n.Prompt, n.Acceptance, labels)
			require.NoError(t, sqlite.RecomputeExportChecksum(data))

			mergeTheirs(t, importer, data)

			got := pendingPayloads(t, importer, "REC-1", tt.op)
			require.NotEmpty(t, got, tt.op)
			joined := fmt.Sprint(got)
			if tt.field != "" {
				require.Contains(t, joined, `"field_name":"`+tt.field+`"`)
			}
			require.Contains(t, joined, tt.want)
			replicate(t, importer, peer)
			require.Equal(t, viewOf(t, importer, "REC-1"), viewOf(t, peer, "REC-1"))
		})
	}
}

// TestImportMerge_DeletedTask_JournalsDeleteAndAPeerConverges: a task the file
// holds deleted is deleted here and journaled with a delete event.
func TestImportMerge_DeletedTask_JournalsDeleteAndAPeerConverges(t *testing.T) {
	importer, peer := peerPair(t, 2)
	markJournalPushed(t, importer)
	data := exportOf(t, importer)
	data.Nodes[1].Title = "Gone"
	data.Nodes[1].ContentHash = plainHash("Gone")
	data.Nodes[1].DeletedAt = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeTheirs(t, importer, data)

	require.Len(t, pendingPayloads(t, importer, "REC-2", "delete"), 1)
	replicate(t, importer, peer)
	require.NotEmpty(t, viewOf(t, peer, "REC-2").DeletedAt, "the peer deleted the task")
}

// TestImportMerge_RestoredTask_StaysDeletedAndIsNamed: a file that restores a
// task deleted here does not write it back (no op restores a task), journals
// nothing, and names the task as one that stays deleted.
func TestImportMerge_RestoredTask_StaysDeletedAndIsNamed(t *testing.T) {
	importer, _ := peerPair(t, 2)
	_, err := importer.WriteDB().Exec(`UPDATE nodes SET deleted_at = '2026-10-01T00:00:00Z' WHERE id = 'REC-2'`)
	require.NoError(t, err)
	markJournalPushed(t, importer)
	data := exportOf(t, importer)
	for i := range data.Nodes {
		if data.Nodes[i].ID == "REC-2" {
			data.Nodes[i].DeletedAt = ""
			data.Nodes[i].Title = "Back"
			data.Nodes[i].ContentHash = plainHash("Back")
		}
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	require.NotEmpty(t, viewOf(t, importer, "REC-2").DeletedAt, "no op restores a deleted task")
	require.Equal(t, []string{"REC-2"}, res.SkippedDeleted)
	require.NotEqual(t, "Back", viewOf(t, importer, "REC-2").Title)
	require.Zero(t, countJournal(t, importer, `sync_status = 'pending'`))
}

// TestImportMerge_NewParentAndChild_CreateEventsAreParentFirst: the file lists
// the child before the parent; the journal creates the parent first.
func TestImportMerge_NewParentAndChild_CreateEventsAreParentFirst(t *testing.T) {
	importer := syncedStoreWithTask(t)
	other := newTestStore(t)
	createPlainTask(t, other, "REC-1", 1, "", sameIDTime)
	createPlainTask(t, other, "REC-2", 2, "", sameIDTime)
	require.NoError(t, other.CreateNode(context.Background(), &model.Node{
		ID: "REC-2.1", ParentID: "REC-2", Project: "REC", Depth: 1, Seq: 1, Title: "Child", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeForDepth(1),
		ContentHash: plainHash("Child"), CreatedAt: sameIDTime, UpdatedAt: sameIDTime}))
	data := exportOf(t, other)
	for i, j := 0, len(data.Nodes)-1; i < j; i, j = i+1, j-1 { // child first
		data.Nodes[i], data.Nodes[j] = data.Nodes[j], data.Nodes[i]
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeFile(t, importer, data)

	var order []string
	rows, err := importer.WriteDB().Query(`SELECT node_id FROM sync_events WHERE op_type = 'create_node' AND sync_status = 'pending' ORDER BY rowid`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		order = append(order, id)
	}
	require.Equal(t, []string{"REC-2", "REC-2.1"}, order)
}

// TestImportMerge_QueueCapBoundary_OneEventUnderTheCapPasses: pending plus the
// import's events equal to the cap is allowed; one more is refused.
func TestImportMerge_QueueCapBoundary_OneEventUnderTheCapPasses(t *testing.T) {
	for _, tc := range []struct {
		limit   int64
		refused bool
	}{{1, false}, {0, false}} {
		s := syncedStoreWithTask(t)
		require.NoError(t, s.SetMaxQueueSize(context.Background(), tc.limit))
		require.NoError(t, mergeErr(s, retitled(t, s, "REC-1", "One event")), "limit %d", tc.limit)
	}
	s := syncedStoreWithTask(t)
	require.NoError(t, s.SetMaxQueueSize(context.Background(), 1))
	_, err := s.WriteDB().Exec(`INSERT INTO sync_events (event_id, project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash, sync_status, created_at)
		VALUES ('00000000-0000-7000-8000-000000000001', 'REC', 'REC-1', 'update_field', '{}', 1, 1, '{}', 'a', '0123456789abcdef', 'pending', '2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	require.Error(t, mergeErr(s, retitled(t, s, "REC-1", "One event")), "1 pending plus 1 event passes a cap of 1")
}

// TestImportMerge_HeldDependency_IsNotJournaledAgain: a dependency the store
// holds already is not a change.
func TestImportMerge_HeldDependency_IsNotJournaledAgain(t *testing.T) {
	s := syncedStoreWithTask(t)
	createPlainTask(t, s, "REC-2", 2, "", sameIDTime)
	require.NoError(t, s.AddDependency(context.Background(), &model.Dependency{FromID: "REC-2", ToID: "REC-1", DepType: model.DepTypeRelated}))
	markJournalPushed(t, s)
	data := exportOf(t, s)
	data.Nodes[0].Title = "Changed"
	data.Nodes[0].ContentHash = plainHash("Changed")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	var raw []byte
	raw, _ = json.Marshal(data.Dependencies)
	require.Contains(t, string(raw), "REC-2")

	mergeFile(t, s, data)

	require.Zero(t, countJournal(t, s, `op_type = 'link_dep' AND sync_status = 'pending'`))
}
