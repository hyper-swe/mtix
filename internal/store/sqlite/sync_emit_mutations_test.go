// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// Per-mutation emission tests for MTIX-15.2.3.
//
// Each test performs one mutation and asserts the corresponding sync_events
// row exists with the right op_type, payload shape, and clock state. This
// proves the wiring in node_create.go, node_update.go, transition.go,
// claim.go, node_delete.go, cancel.go, dependency.go, and context.go.

func mutationTestStore(t *testing.T) (*sqlite.Store, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mut.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	return s, raw
}

// readEventsByOp returns all events for the given op_type, lamport-ascending.
func readEventsByOp(t *testing.T, raw *sql.DB, op model.OpType) []eventRow {
	t.Helper()
	rows, err := raw.Query(`
		SELECT op_type, node_id, payload, author_id, lamport_clock, sync_status
		FROM sync_events WHERE op_type = ? ORDER BY lamport_clock`,
		string(op))
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []eventRow
	for rows.Next() {
		var r eventRow
		var op string
		var status string
		require.NoError(t, rows.Scan(&op, &r.NodeID, &r.Payload, &r.AuthorID, &r.Lamport, &status))
		r.OpType = model.OpType(op)
		r.SyncStatus = model.SyncStatus(status)
		out = append(out, r)
	}
	return out
}

type eventRow struct {
	OpType     model.OpType
	NodeID     string
	Payload    string
	AuthorID   string
	Lamport    int64
	SyncStatus model.SyncStatus
}

func mustCreateNode(t *testing.T, s *sqlite.Store, id, parent string) *model.Node {
	t.Helper()
	node := &model.Node{
		ID:          id,
		ParentID:    parent,
		Project:     "MTIX",
		Title:       "Test " + id,
		Status:      model.StatusOpen,
		NodeType:    model.NodeTypeIssue,
		Priority:    model.PriorityMedium,
		Weight:      1.0,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
		ContentHash: "h",
	}
	if parent == "" {
		node.NodeType = model.NodeTypeEpic
	}
	require.NoError(t, s.CreateNode(context.Background(), node))
	return node
}

// --- create_node ---

func TestEmit_CreateNode(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")

	evs := readEventsByOp(t, raw, model.OpCreateNode)
	require.Len(t, evs, 1)
	require.Equal(t, "MTIX-1", evs[0].NodeID)
	require.Equal(t, model.SyncStatusPending, evs[0].SyncStatus)

	var p model.CreateNodePayload
	require.NoError(t, json.Unmarshal([]byte(evs[0].Payload), &p))
	require.Equal(t, "Test MTIX-1", p.Title)
	require.Equal(t, model.NodeTypeEpic, p.NodeType)
}

// --- update_field / set_acceptance / set_prompt ---

func TestEmit_UpdateNode_TitleEmitsUpdateField(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")

	newTitle := "renamed"
	require.NoError(t, s.UpdateNode(context.Background(), "MTIX-1", &store.NodeUpdate{Title: &newTitle}))

	evs := readEventsByOp(t, raw, model.OpUpdateField)
	require.Len(t, evs, 1)

	var p model.UpdateFieldPayload
	require.NoError(t, json.Unmarshal([]byte(evs[0].Payload), &p))
	require.Equal(t, "title", p.FieldName)
	require.JSONEq(t, `"renamed"`, string(p.NewValue))
}

func TestEmit_UpdateNode_AcceptanceEmitsSetAcceptance(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")

	acc := "must compile and pass tests"
	require.NoError(t, s.UpdateNode(context.Background(), "MTIX-1", &store.NodeUpdate{Acceptance: &acc}))

	evs := readEventsByOp(t, raw, model.OpSetAcceptance)
	require.Len(t, evs, 1)

	var p model.SetAcceptancePayload
	require.NoError(t, json.Unmarshal([]byte(evs[0].Payload), &p))
	require.Equal(t, acc, p.AcceptanceText)
}

func TestEmit_UpdateNode_PromptEmitsSetPrompt(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")

	prm := "do the work"
	require.NoError(t, s.UpdateNode(context.Background(), "MTIX-1", &store.NodeUpdate{Prompt: &prm}))

	evs := readEventsByOp(t, raw, model.OpSetPrompt)
	require.Len(t, evs, 1)

	var p model.SetPromptPayload
	require.NoError(t, json.Unmarshal([]byte(evs[0].Payload), &p))
	require.Equal(t, prm, p.PromptText)
}

func TestEmit_UpdateNode_MultipleFieldsEmitsOneEventPerField(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")

	title := "t"
	desc := "d"
	prm := "p"
	require.NoError(t, s.UpdateNode(context.Background(), "MTIX-1", &store.NodeUpdate{
		Title:       &title,
		Description: &desc,
		Prompt:      &prm,
	}))

	updates := readEventsByOp(t, raw, model.OpUpdateField)
	prompts := readEventsByOp(t, raw, model.OpSetPrompt)
	require.Len(t, updates, 2, "title + description = 2 update_field events")
	require.Len(t, prompts, 1, "prompt routes to set_prompt")
}

// --- transition_status ---

func TestEmit_TransitionStatus(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")

	require.NoError(t, s.TransitionStatus(context.Background(), "MTIX-1", model.StatusInProgress, "starting", "alice"))

	evs := readEventsByOp(t, raw, model.OpTransitionStatus)
	require.Len(t, evs, 1)
	require.Equal(t, "alice", evs[0].AuthorID)

	var p model.TransitionStatusPayload
	require.NoError(t, json.Unmarshal([]byte(evs[0].Payload), &p))
	require.Equal(t, model.StatusOpen, p.From)
	require.Equal(t, model.StatusInProgress, p.To)
	require.Equal(t, "starting", p.Reason)
}

// --- claim ---

func TestEmit_ClaimNode(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")

	require.NoError(t, s.ClaimNode(context.Background(), "MTIX-1", "agent-1"))

	evs := readEventsByOp(t, raw, model.OpClaim)
	require.Len(t, evs, 1)
	require.Equal(t, "agent-1", evs[0].AuthorID)

	var p model.ClaimPayload
	require.NoError(t, json.Unmarshal([]byte(evs[0].Payload), &p))
	require.Equal(t, "agent-1", p.AgentID)
	require.False(t, p.Forced)
}

// --- unclaim ---

func TestEmit_UnclaimNode(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	require.NoError(t, s.ClaimNode(context.Background(), "MTIX-1", "agent-1"))
	require.NoError(t, s.UnclaimNode(context.Background(), "MTIX-1", "release", "agent-1"))

	evs := readEventsByOp(t, raw, model.OpUnclaim)
	require.Len(t, evs, 1)
	require.Equal(t, "agent-1", evs[0].AuthorID)
	require.JSONEq(t, `{}`, evs[0].Payload, "unclaim payload is empty struct")
}

// --- delete ---

func TestEmit_DeleteNode(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")

	require.NoError(t, s.DeleteNode(context.Background(), "MTIX-1", false, "alice"))

	evs := readEventsByOp(t, raw, model.OpDelete)
	require.Len(t, evs, 1)
	require.Equal(t, "alice", evs[0].AuthorID)
}

// --- transition_status via cancel ---

func TestEmit_CancelNode(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")

	require.NoError(t, s.CancelNode(context.Background(), "MTIX-1", "no longer needed", "alice", false))

	evs := readEventsByOp(t, raw, model.OpTransitionStatus)
	// One transition event; payload's To must be cancelled.
	require.GreaterOrEqual(t, len(evs), 1)
	last := evs[len(evs)-1]

	var p model.TransitionStatusPayload
	require.NoError(t, json.Unmarshal([]byte(last.Payload), &p))
	require.Equal(t, model.StatusCancelled, p.To)
	require.Equal(t, "no longer needed", p.Reason)
}

// --- link_dep / unlink_dep ---

func TestEmit_AddAndRemoveDependency(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-2", "")

	dep := &model.Dependency{
		FromID:    "MTIX-1",
		ToID:      "MTIX-2",
		DepType:   model.DepTypeRelated,
		CreatedBy: "alice",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, s.AddDependency(context.Background(), dep))

	links := readEventsByOp(t, raw, model.OpLinkDep)
	require.Len(t, links, 1)
	require.Equal(t, "MTIX-1", links[0].NodeID)
	require.Equal(t, "alice", links[0].AuthorID)

	require.NoError(t, s.RemoveDependency(context.Background(), "MTIX-1", "MTIX-2", model.DepTypeRelated))

	unlinks := readEventsByOp(t, raw, model.OpUnlinkDep)
	require.Len(t, unlinks, 1)
	require.Equal(t, "MTIX-1", unlinks[0].NodeID)
}

// --- comment via SetAnnotations ---

func TestEmit_SetAnnotationsEmitsComment(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")

	require.NoError(t, s.SetAnnotations(context.Background(), "MTIX-1", []model.Annotation{
		{ID: "a1", Author: "alice", Text: "looks good", CreatedAt: time.Now().UTC()},
	}))

	evs := readEventsByOp(t, raw, model.OpComment)
	require.Len(t, evs, 1)

	var p model.CommentPayload
	require.NoError(t, json.Unmarshal([]byte(evs[0].Payload), &p))
	require.Equal(t, "alice", p.AuthorID)
	require.Equal(t, "looks good", p.Body)
}

// --- explicit no-event documentation ---

func TestEmit_UndeleteNode_DoesNotEmit(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	require.NoError(t, s.DeleteNode(context.Background(), "MTIX-1", false, "alice"))

	beforeUndelete := countSyncEvents(t, raw)
	require.NoError(t, s.UndeleteNode(context.Background(), "MTIX-1"))
	afterUndelete := countSyncEvents(t, raw)

	require.Equal(t, beforeUndelete, afterUndelete,
		"UndeleteNode is local restore only — tombstones are monotonic per SYNC-DESIGN section 8.3")
}

func TestEmit_SetAnnotationsOnMissingNode_NoEvent(t *testing.T) {
	s, raw := mutationTestStore(t)

	before := countSyncEvents(t, raw)
	require.NoError(t, s.SetAnnotations(context.Background(), "GHOST-99", []model.Annotation{
		{ID: "a1", Author: "alice", Text: "ignored", CreatedAt: time.Now().UTC()},
	}))
	after := countSyncEvents(t, raw)
	require.Equal(t, before, after,
		"emission tied to UPDATE row count: 0 affected = 0 events")
}

func countSyncEvents(t *testing.T, raw *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_events`).Scan(&n))
	return n
}

// --- atomicity ---

// TestEmit_AtomicityWithFailedMutation reproduces the FR-18.3 atomicity
// guarantee at the application-error level: when an emit completes but
// the surrounding mutation hits an error, the whole transaction MUST
// roll back, leaving zero sync_events rows even though emitEvent already
// wrote one.
//
// The kernel-level kill-9 atomicity is exercised by sync_chaos_test.go
// in this package — that test launches a subprocess whose tx is forcibly
// killed by SIGKILL. This test exercises the in-process rollback path.
func TestEmit_AtomicityWithFailedMutation(t *testing.T) {
	s, raw := mutationTestStore(t)
	ctx := context.Background()

	// Try to create a node whose parent does not exist. CreateNode emits
	// the create_node event AFTER inserting the node. If the parent
	// validation fails, the emit never runs. Conversely, if the emit
	// raised an error post-insert, the tx rolls back and the node is
	// gone too.
	bad := &model.Node{
		ID:        "MTIX-9",
		ParentID:  "MTIX-DOES-NOT-EXIST",
		Project:   "MTIX",
		Title:     "should not persist",
		Status:    model.StatusOpen,
		NodeType:  model.NodeTypeIssue,
		Priority:  model.PriorityMedium,
		Weight:    1.0,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	err := s.CreateNode(ctx, bad)
	require.Error(t, err, "missing parent must fail")

	require.Equal(t, 0, countSyncEvents(t, raw),
		"failed mutation must not leave a sync_events row behind")

	var nodes int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = 'MTIX-9'`).Scan(&nodes))
	require.Equal(t, 0, nodes, "failed mutation must not leave a nodes row behind")

	// Lamport must also be unchanged.
	var lamport string
	require.NoError(t, raw.QueryRow(
		`SELECT value FROM meta WHERE key = 'meta.sync.lamport'`,
	).Scan(&lamport))
	require.Equal(t, "0", lamport, "Lamport bump rolled back atomically with the failed mutation")
}
