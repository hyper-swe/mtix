// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Second-peer tests for MTIX-112.17.16.16 round 2: the events a merge
// journals, applied on a second store, leave it holding what the importer
// holds in every field the merge touched (principle A and B).
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

// replicate applies every pending event of from to to, in journal order, and
// marks them pushed in from, as a push and a pull would.
func replicate(t *testing.T, from, to *sqlite.Store) int {
	t.Helper()
	ctx := context.Background()
	events, err := from.ReadPendingEvents(ctx, 100000)
	require.NoError(t, err)
	for _, e := range events {
		require.NoError(t, to.WithTx(ctx, func(tx *sql.Tx) error { return sqlite.IdempotentApply(ctx, tx, e) }), e.OpType)
	}
	_, err = from.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed'`)
	require.NoError(t, err)
	return len(events)
}

// peerPair returns a synced importer holding REC-1..REC-n and a peer that
// applied the importer's create events.
func peerPair(t *testing.T, n int) (importer, peer *sqlite.Store) {
	t.Helper()
	importer, peer = newTestStore(t), newTestStore(t)
	for i := 1; i <= n; i++ {
		createPlainTask(t, importer, fmt.Sprintf("REC-%d", i), i, "", sameIDTime)
	}
	replicate(t, importer, peer)
	return importer, peer
}

type nodeView struct {
	Title, Description, Prompt, Acceptance, IssueType, Labels, Assignee, Status, DeferUntil, DeletedAt string
	Priority                                                                                           int
}

func viewOf(t *testing.T, s *sqlite.Store, id string) nodeView {
	t.Helper()
	var v nodeView
	require.NoError(t, s.WriteDB().QueryRow(`SELECT title, COALESCE(description,''), COALESCE(prompt,''),
		COALESCE(acceptance,''), COALESCE(issue_type,''), COALESCE(labels,'[]'), COALESCE(assignee,''), status,
		COALESCE(defer_until,''), COALESCE(deleted_at,''), priority FROM nodes WHERE id = ?`, id).Scan(
		&v.Title, &v.Description, &v.Prompt, &v.Acceptance, &v.IssueType, &v.Labels, &v.Assignee, &v.Status,
		&v.DeferUntil, &v.DeletedAt, &v.Priority))
	return v
}

func mergeTheirs(t *testing.T, s *sqlite.Store, data *sqlite.ExportData) *sqlite.ImportResult {
	t.Helper()
	_, res, err := s.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Workflow: sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs}})
	require.NoError(t, err)
	return res
}

// plainHash is the content hash of a task with no labels.
func plainHash(title string) string { return model.ComputeContentHash(title, "", "", "", nil) }

// TestImportMerge_SecondPeer_ConvergesOnEveryJournaledField: the importer and
// a peer that applies the journal agree on every field the merge changed,
// including a wake time set with a deferral and a wake-time-only change.
func TestImportMerge_SecondPeer_ConvergesOnEveryJournaledField(t *testing.T) {
	importer, peer := peerPair(t, 4)
	wake := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC).Format(time.RFC3339)
	wake2 := time.Date(2032, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339)
	for _, s := range []*sqlite.Store{importer, peer} { // REC-4 is already deferred on both
		_, err := s.WriteDB().Exec(`UPDATE nodes SET status = 'deferred', defer_until = ? WHERE id = 'REC-4'`, wake)
		require.NoError(t, err)
	}
	data := exportOf(t, importer)
	for i := range data.Nodes {
		n := &data.Nodes[i]
		switch n.ID {
		case "REC-1":
			n.Title, n.Description, n.Prompt, n.Acceptance = "T1", "D1", "P1", "A1"
			n.Priority, n.IssueType, n.Labels, n.Assignee = 1, string(model.IssueTypeBug), `["x","y"]`, "alice"
			n.ContentHash = model.ComputeContentHash("T1", "D1", "P1", "A1", []string{"x", "y"})
		case "REC-3":
			n.Title, n.Status, n.DeferUntil = "T3", string(model.StatusDeferred), wake
			n.ContentHash = plainHash(n.Title)
		case "REC-4":
			n.Title, n.DeferUntil = "T4", wake2
			n.ContentHash = plainHash(n.Title)
		}
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	markJournalPushed(t, importer)

	mergeTheirs(t, importer, data)
	replicate(t, importer, peer)

	for _, id := range []string{"REC-1", "REC-2", "REC-3", "REC-4"} {
		require.Equal(t, viewOf(t, importer, id), viewOf(t, peer, id), id)
	}
	require.Equal(t, wake, viewOf(t, peer, "REC-3").DeferUntil, "the wake time of a deferral reaches the peer")
	require.Equal(t, wake2, viewOf(t, peer, "REC-4").DeferUntil, "a wake-time-only change reaches the peer")
}

// TestImportMerge_ColumnsNoOpCarries_AreNamedLocalOnlyByField: every column
// the merge writes that no event carries is named, per task, in the result.
func TestImportMerge_ColumnsNoOpCarries_AreNamedLocalOnlyByField(t *testing.T) {
	importer, _ := peerPair(t, 1)
	markJournalPushed(t, importer)
	data := exportOf(t, importer)
	est, act := 30, 40
	n := &data.Nodes[0]
	n.Title, n.Weight, n.EstimateMin, n.ActualMin = "T1", 2.5, &est, &act
	n.Metadata, n.SessionID = `{"k":1}`, "sess-1"
	n.CodeRefs = []model.CodeRef{{File: "a.go"}}
	n.CommitRefs = []string{"abc123"}
	n.InvalidatedAt, n.InvalidatedBy, n.InvalidationReason = "2026-01-01T00:00:00Z", "bob", "stale"
	n.Annotations = []model.Annotation{{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Author: "bob", Text: "", CreatedAt: sameIDTime}}
	n.ContentHash = plainHash(n.Title)
	n2 := *n
	n2.ID, n2.Seq, n2.UID, n2.Title = "REC-2", 2, "", "T2"
	n2.AgentState, n2.Weight, n2.Annotations = "thinking", 3, nil
	n2.EstimateMin, n2.ActualMin, n2.Metadata, n2.SessionID, n2.CodeRefs, n2.CommitRefs = nil, nil, "", "", nil, nil
	n2.InvalidatedAt, n2.InvalidatedBy, n2.InvalidationReason = "", "", ""
	n2.ContentHash = plainHash(n2.Title)
	data.Nodes = append(data.Nodes, n2)
	data.NodeCount = len(data.Nodes)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	require.Equal(t, []sqlite.LocalOnlyChange{
		{ID: "REC-1", Fields: []string{"actual_min", "annotations", "code_refs", "commit_refs", "estimate_min",
			"invalidation", "metadata", "session_id", "weight"}},
		{ID: "REC-2", Fields: []string{"agent_state", "weight"}},
	}, res.JournalLocalOnly)
}
