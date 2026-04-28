// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"
)

// applyTestStore opens a fresh v2 store for apply-engine tests.
func applyTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "apply.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return s, raw
}

// makeApplyEvent builds a valid SyncEvent for a given op_type and
// payload. Caller adjusts fields as needed for individual tests.
func makeApplyEvent(t *testing.T, op model.OpType, nodeID, author string, lamport int64, payload any) *model.SyncEvent {
	t.Helper()
	pl, err := model.EncodePayload(payload)
	require.NoError(t, err)
	return &model.SyncEvent{
		EventID:           clock.MustNewEventID(),
		ProjectPrefix:     "MTIX",
		NodeID:            nodeID,
		OpType:            op,
		Payload:           pl,
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      lamport,
		VectorClock:       model.VectorClock{author: lamport},
		AuthorID:          author,
		AuthorMachineHash: "0123456789abcdef",
		SyncStatus:        model.SyncStatusApplied,
	}
}

// applyOnce runs IdempotentApply inside a fresh tx and returns the
// resulting error. Used so tests don't have to manage tx scope.
func applyOnce(t *testing.T, s *Store, e *model.SyncEvent) error {
	t.Helper()
	return s.WithTx(context.Background(), func(tx *sql.Tx) error {
		return IdempotentApply(context.Background(), tx, e)
	})
}

func countNodes(t *testing.T, raw *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM nodes WHERE deleted_at IS NULL`).Scan(&n))
	return n
}

func countEvents(t *testing.T, raw *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_events`).Scan(&n))
	return n
}

func countApplied(t *testing.T, raw *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM applied_events`).Scan(&n))
	return n
}

func readNodeColumn(t *testing.T, raw *sql.DB, id, column string) string {
	t.Helper()
	var v sql.NullString
	require.NoError(t, raw.QueryRow(
		`SELECT `+column+` FROM nodes WHERE id = ?`, id,
	).Scan(&v))
	return v.String
}

// --- Core invariants ---

func TestApply_NilEvent(t *testing.T) {
	s, _ := applyTestStore(t)
	err := applyOnce(t, s, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}

func TestApply_RejectsInvalidEvent(t *testing.T) {
	s, _ := applyTestStore(t)
	e := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})
	e.AuthorID = "" // breaks model.SyncEvent.Validate
	err := applyOnce(t, s, e)
	require.Error(t, err)
}

func TestApply_NoSyncEventEmittedByApply(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	require.Equal(t, 0, countEvents(t, raw),
		"apply MUST NOT emit a sync_event (would loop forever)")
}

func TestApply_DuplicateEventIDIsNoop(t *testing.T) {
	s, raw := applyTestStore(t)
	e := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})
	require.NoError(t, applyOnce(t, s, e))
	require.NoError(t, applyOnce(t, s, e))
	require.NoError(t, applyOnce(t, s, e))
	require.Equal(t, 1, countNodes(t, raw))
	require.Equal(t, 1, countApplied(t, raw),
		"applied_events MUST have exactly one row regardless of replay count")
}

// --- create_node ---

func TestApply_CreateNode_HappyPath(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "hello", Description: "d", Priority: model.Priority(2)})))
	require.Equal(t, "hello", readNodeColumn(t, raw, "MTIX-1", "title"))
}

func TestApply_CreateNode_CanonicalizesNodeType(t *testing.T) {
	s, raw := applyTestStore(t)
	// Synthetic event with depth=0 (no parent) but payload claiming
	// node_type='story'. FR-18.10: apply must override with the
	// canonical NodeTypeForDepth value (epic).
	e := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x", NodeType: model.NodeTypeStory})
	require.NoError(t, applyOnce(t, s, e))
	require.Equal(t, string(model.NodeTypeEpic), readNodeColumn(t, raw, "MTIX-1", "node_type"),
		"apply MUST canonicalize node_type per NodeTypeForDepth(depth)")
}

func TestApply_CreateNode_DepthFromParent(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "p"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1.2", "alice", 2,
		&model.CreateNodePayload{Title: "c", ParentID: "MTIX-1"})))
	require.Equal(t, string(model.NodeTypeStory), readNodeColumn(t, raw, "MTIX-1.2", "node_type"),
		"depth=1 -> story per NodeTypeForDepth")
}

// --- update_field ---

func TestApply_UpdateField_Title(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "old"})))

	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2,
		&model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"new"`)})))
	require.Equal(t, "new", readNodeColumn(t, raw, "MTIX-1", "title"))
}

func TestApply_UpdateField_Priority(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x", Priority: model.Priority(3)})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2,
		&model.UpdateFieldPayload{FieldName: "priority", NewValue: json.RawMessage(`5`)})))
	require.Equal(t, "5", readNodeColumn(t, raw, "MTIX-1", "priority"))
}

func TestApply_UpdateField_OnMissingNode(t *testing.T) {
	s, _ := applyTestStore(t)
	err := applyOnce(t, s, makeApplyEvent(t, model.OpUpdateField, "GHOST-1", "alice", 1,
		&model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"x"`)}))
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrNotFound))
}

func TestApply_UpdateField_RejectsNonWhitelistedField(t *testing.T) {
	s, _ := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	err := applyOnce(t, s, makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2,
		&model.UpdateFieldPayload{FieldName: "id; DROP TABLE nodes--", NewValue: json.RawMessage(`"x"`)}))
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}

// --- transition_status ---

func TestApply_TransitionStatus(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpTransitionStatus, "MTIX-1", "alice", 2,
		&model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusInProgress})))
	require.Equal(t, string(model.StatusInProgress), readNodeColumn(t, raw, "MTIX-1", "status"))
}

func TestApply_TransitionStatus_TerminalSetsClosedAt(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpTransitionStatus, "MTIX-1", "alice", 2,
		&model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusDone})))
	require.NotEmpty(t, readNodeColumn(t, raw, "MTIX-1", "closed_at"))
}

// --- claim / unclaim ---

func TestApply_Claim(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpClaim, "MTIX-1", "agent-1", 2,
		&model.ClaimPayload{AgentID: "agent-1"})))
	require.Equal(t, "agent-1", readNodeColumn(t, raw, "MTIX-1", "assignee"))
	require.Equal(t, string(model.StatusInProgress), readNodeColumn(t, raw, "MTIX-1", "status"))
}

func TestApply_Unclaim(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpClaim, "MTIX-1", "agent-1", 2,
		&model.ClaimPayload{AgentID: "agent-1"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpUnclaim, "MTIX-1", "agent-1", 3,
		&model.UnclaimPayload{})))
	require.Equal(t, "", readNodeColumn(t, raw, "MTIX-1", "assignee"))
	require.Equal(t, string(model.StatusOpen), readNodeColumn(t, raw, "MTIX-1", "status"))
}

// --- delete ---

func TestApply_Delete(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpDelete, "MTIX-1", "alice", 2,
		&model.DeletePayload{})))
	require.NotEmpty(t, readNodeColumn(t, raw, "MTIX-1", "deleted_at"))
}

func TestApply_Delete_OnNonExistentNode_NoPhantomTombstone(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpDelete, "GHOST-99", "alice", 1,
		&model.DeletePayload{})))
	require.Equal(t, 0, countNodes(t, raw),
		"delete on non-existent node MUST NOT create a phantom tombstone row")
}

// --- link_dep / unlink_dep ---

func TestApply_LinkAndUnlinkDep(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "p"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-2", "alice", 2,
		&model.CreateNodePayload{Title: "q"})))

	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpLinkDep, "MTIX-1", "alice", 3,
		&model.LinkDepPayload{DependsOnNodeID: "MTIX-2", DepType: "blocks"})))
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM dependencies WHERE from_id='MTIX-1' AND to_id='MTIX-2'`).Scan(&n))
	require.Equal(t, 1, n)

	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpUnlinkDep, "MTIX-1", "alice", 4,
		&model.UnlinkDepPayload{DependsOnNodeID: "MTIX-2", DepType: "blocks"})))
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM dependencies WHERE from_id='MTIX-1' AND to_id='MTIX-2'`).Scan(&n))
	require.Equal(t, 0, n)
}

// --- comment ---

func TestApply_Comment_AppendsAnnotation(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpComment, "MTIX-1", "alice", 2,
		&model.CommentPayload{AuthorID: "alice", Body: "looks good"})))
	require.Contains(t, readNodeColumn(t, raw, "MTIX-1", "annotations"), "looks good")
}

// --- set_acceptance / set_prompt ---

func TestApply_SetAcceptance(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpSetAcceptance, "MTIX-1", "alice", 2,
		&model.SetAcceptancePayload{AcceptanceText: "must compile"})))
	require.Equal(t, "must compile", readNodeColumn(t, raw, "MTIX-1", "acceptance"))
}

func TestApply_SetPrompt(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpSetPrompt, "MTIX-1", "alice", 2,
		&model.SetPromptPayload{PromptText: "do the thing"})))
	require.Equal(t, "do the thing", readNodeColumn(t, raw, "MTIX-1", "prompt"))
}

// --- defer ---

func TestApply_Defer(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	until := time.Now().Add(7 * 24 * time.Hour).UTC()
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpDefer, "MTIX-1", "alice", 2,
		&model.DeferPayload{Reason: "wait", Until: &until})))
	require.Equal(t, string(model.StatusDeferred), readNodeColumn(t, raw, "MTIX-1", "status"))
	require.NotEmpty(t, readNodeColumn(t, raw, "MTIX-1", "defer_until"))
}

// --- clock advance / VC merge ---

func TestApply_LamportAdvances(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 5,
		&model.CreateNodePayload{Title: "x"})))
	var raw2 string
	require.NoError(t, raw.QueryRow(`SELECT value FROM meta WHERE key='meta.sync.lamport'`).Scan(&raw2))
	require.Equal(t, "5", raw2)
}

func TestApply_LamportNeverGoesBackwards(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 10,
		&model.CreateNodePayload{Title: "x"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "bob", 3,
		&model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"y"`)})))
	var raw2 string
	require.NoError(t, raw.QueryRow(`SELECT value FROM meta WHERE key='meta.sync.lamport'`).Scan(&raw2))
	require.Equal(t, "10", raw2, "Lamport never rewinds")
}

func TestApply_VectorClockMerges(t *testing.T) {
	s, raw := applyTestStore(t)
	e1 := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})
	e1.VectorClock = model.VectorClock{"alice": 1}
	require.NoError(t, applyOnce(t, s, e1))

	e2 := makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "bob", 2,
		&model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"y"`)})
	e2.VectorClock = model.VectorClock{"alice": 1, "bob": 5}
	require.NoError(t, applyOnce(t, s, e2))

	var raw2 string
	require.NoError(t, raw.QueryRow(`SELECT value FROM meta WHERE key='meta.sync.vector_clock'`).Scan(&raw2))
	var got model.VectorClock
	require.NoError(t, json.Unmarshal([]byte(raw2), &got))
	require.Equal(t, int64(1), got["alice"])
	require.Equal(t, int64(5), got["bob"])
}

// --- helpers ---

func TestComputeDepth(t *testing.T) {
	cases := []struct {
		parent string
		want   int
	}{
		{"", 0},
		{"MTIX-1", 1},
		{"MTIX-1.2", 2},
		{"MTIX-1.2.3", 3},
	}
	for _, tc := range cases {
		require.Equalf(t, tc.want, computeDepth(tc.parent), "parent %q", tc.parent)
	}
}

func TestDeriveSeq(t *testing.T) {
	require.Equal(t, 1, deriveSeq("MTIX-1"))
	require.Equal(t, 42, deriveSeq("MTIX-1.42"))
	require.Equal(t, 0, deriveSeq("malformed"))
	require.Equal(t, 0, deriveSeq(""))
}

// withMalformedPayload exercises the json.Unmarshal error path inside
// each per-op apply function. We construct a valid envelope and
// inject a malformed payload — the validator passes (payload is
// non-empty) but the per-op decoder fails.
func TestApply_MalformedPayloadPerOpType(t *testing.T) {
	cases := []struct {
		op   model.OpType
		need string // substring expected in error
	}{
		{model.OpCreateNode, "create_node"},
		{model.OpUpdateField, "update_field"},
		{model.OpTransitionStatus, "transition_status"},
		{model.OpClaim, "claim"},
		{model.OpDefer, "defer"},
		{model.OpComment, "comment"},
		{model.OpLinkDep, "link_dep"},
		{model.OpUnlinkDep, "unlink_dep"},
		{model.OpSetAcceptance, "set_acceptance"},
		{model.OpSetPrompt, "set_prompt"},
	}
	for _, tc := range cases {
		t.Run(string(tc.op), func(t *testing.T) {
			s, _ := applyTestStore(t)
			// Pre-create the node so requireNodeExists doesn't trip
			// before the decode attempt.
			require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
				&model.CreateNodePayload{Title: "x"})))

			e := makeApplyEvent(t, tc.op, "MTIX-1", "alice", 2, nil)
			e.Payload = json.RawMessage(`<<<not-json`)
			err := applyOnce(t, s, e)
			require.Error(t, err)
			require.Contains(t, err.Error(), "decode payload")
			require.Contains(t, err.Error(), tc.need)
		})
	}
}

func TestApply_UnknownOpType(t *testing.T) {
	s, _ := applyTestStore(t)
	e := makeApplyEvent(t, model.OpType("bogus"), "MTIX-1", "alice", 1, &model.UnclaimPayload{})
	err := applyOnce(t, s, e)
	require.Error(t, err)
	// Validate catches it first since OpType is in the AllOpTypes check.
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}

func TestApply_MalformedLocalLamportSurfaceError(t *testing.T) {
	s, raw := applyTestStore(t)
	_, err := raw.Exec(`UPDATE meta SET value = 'not-an-int' WHERE key = 'meta.sync.lamport'`)
	require.NoError(t, err)
	e := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "x"})
	err = applyOnce(t, s, e)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse current lamport")
}

func TestApply_MalformedLocalVectorClockSurfaceError(t *testing.T) {
	s, raw := applyTestStore(t)
	_, err := raw.Exec(`UPDATE meta SET value = '<<<not-json' WHERE key = 'meta.sync.vector_clock'`)
	require.NoError(t, err)
	e := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "x"})
	err = applyOnce(t, s, e)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse local VC")
}
