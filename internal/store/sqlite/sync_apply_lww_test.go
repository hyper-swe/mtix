// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"
)

// MTIX-15.5.1 LWW resolution tests. These exercise the apply engine's
// behavior when two events compete for the same logical field on the
// same node.

func makeUpdateFieldEvent(t *testing.T, nodeID, author, field, valueJSON string, lamport int64, wallTS int64, hash string) *model.SyncEvent {
	t.Helper()
	pl, err := model.EncodePayload(&model.UpdateFieldPayload{
		FieldName: field,
		NewValue:  json.RawMessage(valueJSON),
	})
	require.NoError(t, err)
	return &model.SyncEvent{
		EventID:           clock.MustNewEventID(),
		ProjectPrefix:     "MTIX",
		NodeID:            nodeID,
		OpType:            model.OpUpdateField,
		Payload:           pl,
		WallClockTS:       wallTS,
		LamportClock:      lamport,
		VectorClock:       model.VectorClock{author: lamport},
		AuthorID:          author,
		AuthorMachineHash: hash,
		SyncStatus:        model.SyncStatusApplied,
	}
}

func createNodeFor(t *testing.T, s *Store, id string) {
	t.Helper()
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, id, "alice", 1,
		&model.CreateNodePayload{Title: "init"})))
}

func conflictRows(t *testing.T, raw *sql.DB) []conflictRow {
	t.Helper()
	rows, err := raw.Query(`
		SELECT event_id_winner, event_id_loser, node_id, field_name, resolution
		FROM sync_conflicts ORDER BY conflict_id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []conflictRow
	for rows.Next() {
		var r conflictRow
		var field sql.NullString
		require.NoError(t, rows.Scan(&r.Winner, &r.Loser, &r.NodeID, &field, &r.Resolution))
		r.FieldName = field.String
		out = append(out, r)
	}
	return out
}

type conflictRow struct {
	Winner     string
	Loser      string
	NodeID     string
	FieldName  string
	Resolution string
}

// --- LWW basics ---

func TestApplyLWW_HigherLamportWins(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")

	e1 := makeUpdateFieldEvent(t, "MTIX-1", "alice", "title", `"first"`, 5, 100, "0123456789abcdef")
	require.NoError(t, applyOnce(t, s, e1))
	require.Equal(t, "first", readNodeColumn(t, raw, "MTIX-1", "title"))

	e2 := makeUpdateFieldEvent(t, "MTIX-1", "bob", "title", `"second"`, 10, 200, "fedcba9876543210")
	require.NoError(t, applyOnce(t, s, e2))
	require.Equal(t, "second", readNodeColumn(t, raw, "MTIX-1", "title"),
		"higher lamport wins")

	confs := conflictRows(t, raw)
	require.Len(t, confs, 1)
	require.Equal(t, e2.EventID, confs[0].Winner)
	require.Equal(t, e1.EventID, confs[0].Loser)
	require.Equal(t, "title", confs[0].FieldName)
}

func TestApplyLWW_LowerLamportLoses(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")

	winner := makeUpdateFieldEvent(t, "MTIX-1", "alice", "title", `"high"`, 10, 200, "0123456789abcdef")
	require.NoError(t, applyOnce(t, s, winner))
	require.Equal(t, "high", readNodeColumn(t, raw, "MTIX-1", "title"))

	loser := makeUpdateFieldEvent(t, "MTIX-1", "bob", "title", `"low"`, 5, 100, "fedcba9876543210")
	require.NoError(t, applyOnce(t, s, loser))
	require.Equal(t, "high", readNodeColumn(t, raw, "MTIX-1", "title"),
		"lower lamport must NOT overwrite the higher-lamport prior")

	confs := conflictRows(t, raw)
	require.Len(t, confs, 1)
	require.Equal(t, winner.EventID, confs[0].Winner)
	require.Equal(t, loser.EventID, confs[0].Loser)

	// Loser STILL appears in applied_events so re-pulls are no-ops.
	var n int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, loser.EventID,
	).Scan(&n))
	require.Equal(t, 1, n)
}

func TestApplyLWW_SameLamportTieBreakByWallClockTS(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")

	e1 := makeUpdateFieldEvent(t, "MTIX-1", "alice", "title", `"a"`, 5, 100, "0123456789abcdef")
	require.NoError(t, applyOnce(t, s, e1))

	e2 := makeUpdateFieldEvent(t, "MTIX-1", "bob", "title", `"b"`, 5, 200, "fedcba9876543210")
	require.NoError(t, applyOnce(t, s, e2))
	require.Equal(t, "b", readNodeColumn(t, raw, "MTIX-1", "title"),
		"equal lamport: higher wall_clock_ts wins")
}

func TestApplyLWW_SameLamportSameTSTieBreakByMachineHash(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")

	e1 := makeUpdateFieldEvent(t, "MTIX-1", "alice", "title", `"zzz-applied-first"`, 5, 100, "ffffffffffffffff")
	require.NoError(t, applyOnce(t, s, e1))

	e2 := makeUpdateFieldEvent(t, "MTIX-1", "bob", "title", `"aaa-lower-hash"`, 5, 100, "0000000000000000")
	require.NoError(t, applyOnce(t, s, e2))
	require.Equal(t, "aaa-lower-hash", readNodeColumn(t, raw, "MTIX-1", "title"),
		"equal lamport+ts: lower author_machine_hash wins (lex compare)")
}

func TestApplyLWW_DisjointFieldsNoConflict(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")

	require.NoError(t, applyOnce(t, s, makeUpdateFieldEvent(t,
		"MTIX-1", "alice", "title", `"new-title"`, 5, 100, "0123456789abcdef")))
	require.NoError(t, applyOnce(t, s, makeUpdateFieldEvent(t,
		"MTIX-1", "bob", "description", `"new-desc"`, 5, 100, "fedcba9876543210")))

	require.Equal(t, "new-title", readNodeColumn(t, raw, "MTIX-1", "title"))
	require.Equal(t, "new-desc", readNodeColumn(t, raw, "MTIX-1", "description"))
	require.Empty(t, conflictRows(t, raw),
		"disjoint fields produce no conflict")
}

func TestApplyLWW_OrderInvariant(t *testing.T) {
	// Apply (e1, e2) against storeA; apply (e2, e1) against storeB.
	// Both stores must converge to the same final value AND record
	// the same conflict (same winner/loser pair).
	storeA, rawA := applyTestStore(t)
	createNodeFor(t, storeA, "MTIX-1")
	e1 := makeUpdateFieldEvent(t, "MTIX-1", "alice", "title", `"alice-val"`, 5, 100, "0123456789abcdef")
	e2 := makeUpdateFieldEvent(t, "MTIX-1", "bob", "title", `"bob-val"`, 10, 200, "fedcba9876543210")
	require.NoError(t, applyOnce(t, storeA, e1))
	require.NoError(t, applyOnce(t, storeA, e2))

	storeB, rawB := applyTestStore(t)
	createNodeFor(t, storeB, "MTIX-1")
	require.NoError(t, applyOnce(t, storeB, e2))
	require.NoError(t, applyOnce(t, storeB, e1))

	// Final value identical.
	require.Equal(t,
		readNodeColumn(t, rawA, "MTIX-1", "title"),
		readNodeColumn(t, rawB, "MTIX-1", "title"),
		"order-invariant: both stores converge to the same field value")

	// Same conflict recorded (winner+loser pair).
	confsA := conflictRows(t, rawA)
	confsB := conflictRows(t, rawB)
	require.Len(t, confsA, 1)
	require.Len(t, confsB, 1)
	require.Equal(t, confsA[0].Winner, confsB[0].Winner)
	require.Equal(t, confsA[0].Loser, confsB[0].Loser)
}

func TestApplyLWW_SetAcceptanceConflictsByOpType(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")

	e1 := makeApplyEvent(t, model.OpSetAcceptance, "MTIX-1", "alice", 5,
		&model.SetAcceptancePayload{AcceptanceText: "first"})
	e1.WallClockTS = 100
	e1.AuthorMachineHash = "0123456789abcdef"
	require.NoError(t, applyOnce(t, s, e1))

	e2 := makeApplyEvent(t, model.OpSetAcceptance, "MTIX-1", "bob", 10,
		&model.SetAcceptancePayload{AcceptanceText: "second"})
	e2.WallClockTS = 200
	e2.AuthorMachineHash = "fedcba9876543210"
	require.NoError(t, applyOnce(t, s, e2))

	require.Equal(t, "second", readNodeColumn(t, raw, "MTIX-1", "acceptance"))
	require.Len(t, conflictRows(t, raw), 1)
}

func TestApplyLWW_DeleteThenUpdateLosesSilently(t *testing.T) {
	// SYNC-DESIGN section 8.3: tombstones are monotonic. After delete
	// applies, subsequent updates fail with ErrNotFound (the WHERE
	// clause excludes deleted rows). No conflict row recorded — the
	// monotonic tombstone semantics cover this case without LWW.
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")

	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpDelete, "MTIX-1", "alice", 5,
		&model.DeletePayload{})))

	upd := makeUpdateFieldEvent(t, "MTIX-1", "bob", "title", `"x"`, 10, 200, "fedcba9876543210")
	err := applyOnce(t, s, upd)
	require.Error(t, err, "update on tombstoned node MUST fail")

	require.Empty(t, conflictRows(t, raw),
		"tombstone-vs-update is not LWW; monotonic semantics suffice")
}

func TestApplyLWW_UpdateFieldOnDifferentNodesDoesntInteract(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")
	createNodeFor(t, s, "MTIX-2")

	e1 := makeUpdateFieldEvent(t, "MTIX-1", "alice", "title", `"a"`, 5, 100, "0123456789abcdef")
	e2 := makeUpdateFieldEvent(t, "MTIX-2", "bob", "title", `"b"`, 5, 100, "fedcba9876543210")
	require.NoError(t, applyOnce(t, s, e1))
	require.NoError(t, applyOnce(t, s, e2))

	require.Equal(t, "a", readNodeColumn(t, raw, "MTIX-1", "title"))
	require.Equal(t, "b", readNodeColumn(t, raw, "MTIX-2", "title"))
	require.Empty(t, conflictRows(t, raw),
		"different nodes are independent — no conflict")
}

func TestIncomingBeats_AllOrderings(t *testing.T) {
	cases := []struct {
		name                string
		eLamp, eTS          int64
		eHash               string
		priorLamp, priorTS  int64
		priorHash           string
		expectIncomingBeats bool
	}{
		{"higher lamport wins", 10, 100, "z", 5, 200, "a", true},
		{"lower lamport loses", 5, 200, "a", 10, 100, "z", false},
		{"equal lamport higher TS wins", 5, 200, "z", 5, 100, "a", true},
		{"equal lamport lower TS loses", 5, 100, "a", 5, 200, "z", false},
		{"all equal except lower hash wins", 5, 100, "aaa", 5, 100, "zzz", true},
		{"all equal except higher hash loses", 5, 100, "zzz", 5, 100, "aaa", false},
		{"all-equal: prior keeps", 5, 100, "abc", 5, 100, "abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &model.SyncEvent{
				LamportClock:      tc.eLamp,
				WallClockTS:       tc.eTS,
				AuthorMachineHash: tc.eHash,
			}
			got := incomingBeats(e, tc.priorLamp, tc.priorTS, tc.priorHash)
			require.Equal(t, tc.expectIncomingBeats, got)
		})
	}
}

func TestFieldKeyForLWW(t *testing.T) {
	cases := []struct {
		op      model.OpType
		payload any
		want    string
	}{
		{model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "title"}, "update_field:title"},
		{model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "description"}, "update_field:description"},
		{model.OpSetAcceptance, &model.SetAcceptancePayload{AcceptanceText: "x"}, "set_acceptance:acceptance"},
		{model.OpSetPrompt, &model.SetPromptPayload{PromptText: "y"}, "set_prompt:prompt"},
		{model.OpCreateNode, &model.CreateNodePayload{Title: "n"}, ""},
		{model.OpClaim, &model.ClaimPayload{AgentID: "a"}, ""},
		{model.OpDelete, &model.DeletePayload{}, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.op), func(t *testing.T) {
			pl, err := model.EncodePayload(tc.payload)
			require.NoError(t, err)
			e := &model.SyncEvent{OpType: tc.op, Payload: pl}
			require.Equal(t, tc.want, fieldKeyForLWW(e))
		})
	}
}

func TestFieldKeyForLWW_MalformedUpdateFieldPayload(t *testing.T) {
	e := &model.SyncEvent{OpType: model.OpUpdateField, Payload: []byte(`<<<not-json`)}
	require.Equal(t, "", fieldKeyForLWW(e),
		"malformed payload returns empty key; the validator catches the malformed payload separately")
}

// Apply-time wall_clock matters: if two replicas see the same event
// set at different wall clock times, the conflicts.log written
// timestamps differ but the sync_conflicts row content (winner/loser/
// node/field) matches. This is what enables convergence in 15.5.3.
func TestApplyLWW_ConflictRowDeterministicAcrossOrders(t *testing.T) {
	storeA, rawA := applyTestStore(t)
	storeB, rawB := applyTestStore(t)
	createNodeFor(t, storeA, "MTIX-1")
	createNodeFor(t, storeB, "MTIX-1")

	e1 := makeUpdateFieldEvent(t, "MTIX-1", "alice", "title", `"a"`, 5, 100, "1111111111111111")
	e2 := makeUpdateFieldEvent(t, "MTIX-1", "bob", "title", `"b"`, 10, 200, "2222222222222222")

	require.NoError(t, applyOnce(t, storeA, e1))
	require.NoError(t, applyOnce(t, storeA, e2))

	// Pause briefly so any timestamp comparison would diverge.
	time.Sleep(2 * time.Millisecond)

	require.NoError(t, applyOnce(t, storeB, e2))
	require.NoError(t, applyOnce(t, storeB, e1))

	confsA := conflictRows(t, rawA)
	confsB := conflictRows(t, rawB)
	require.Equal(t, confsA[0].Winner, confsB[0].Winner)
	require.Equal(t, confsA[0].Loser, confsB[0].Loser)
	require.Equal(t, confsA[0].NodeID, confsB[0].NodeID)
	require.Equal(t, confsA[0].FieldName, confsB[0].FieldName)

	// Verify the wall-clock-context awareness:
	// The fact that resolved_at differs is fine — that's the LOCAL
	// observation time. The semantic conflict identity is the same.
	_, _ = context.Background(), 0
}
