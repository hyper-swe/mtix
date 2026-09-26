// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// PG-gated tests of push acknowledgement by presence (MTIX-95.3, ADR-006
// D7/D48, review F-80). An event whose id is already on the hub (its first
// push committed, but the pusher never marked it pushed) used to be
// reported not accepted, because its INSERT ... ON CONFLICT (event_id) DO
// NOTHING inserted no row; it stayed pending forever. Skip when
// MTIX_PG_TEST_DSN is unset.

// presenceEvent returns a valid event of op for nodeID with payload.
func presenceEvent(id, nodeID string, op model.OpType, payload string, lamport int64) *model.SyncEvent {
	e := makeEvent(id, nodeID, "alice", lamport)
	e.OpType = op
	e.Payload = json.RawMessage(payload)
	return e
}

// presenceBatch is a batch of three kinds of event: a task's creation, a
// field update of it, and a second task's creation.
func presenceBatch() []*model.SyncEvent {
	return []*model.SyncEvent{
		presenceEvent("0193fa00-0000-7000-8000-000000095301", "MTIX-1", model.OpCreateNode,
			`{"title":"first"}`, 1),
		presenceEvent("0193fa00-0000-7000-8000-000000095302", "MTIX-1", model.OpUpdateField,
			`{"field_name":"title","new_value":"renamed"}`, 2),
		presenceEvent("0193fa00-0000-7000-8000-000000095303", "MTIX-2", model.OpCreateNode,
			`{"title":"second"}`, 3),
	}
}

// eventIDs returns the ids of events, in order.
func eventIDs(events []*model.SyncEvent) []string {
	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.EventID
	}
	return ids
}

// hubRows returns how many hub rows hold one of ids.
func hubRows(t *testing.T, pool *transport.Pool, ids []string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.Inner().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sync_events WHERE event_id = ANY($1)`, ids).Scan(&n))
	return n
}

// TestPushEvents_AlreadyPresent_ReportedAccepted: re-pushing events whose
// ids are already on the hub, with the same node, op and payload, reports
// each one accepted as already present, inserts nothing, and still inserts
// the new events of the same batch. A payload that differs only in key
// order and spacing is the same payload. The legacy PushEvents* views
// report them accepted too (MTIX-95.3 acceptance 1).
func TestPushEvents_AlreadyPresent_ReportedAccepted(t *testing.T) {
	fresh := []*model.SyncEvent{
		presenceEvent("0193fa00-0000-7000-8000-000000095311", "MTIX-3", model.OpCreateNode,
			`{"title":"third"}`, 4),
		presenceEvent("0193fa00-0000-7000-8000-000000095312", "MTIX-3", model.OpComment,
			`{"body":"hello"}`, 5),
	}
	respaced := presenceBatch()
	respaced[1].Payload = json.RawMessage(`{ "new_value" : "renamed",  "field_name" : "title" }`)
	tests := []struct {
		name         string
		repush       []*model.SyncEvent
		wantPresent  []string
		wantInserted []string
	}{
		{"the whole batch again", presenceBatch(), eventIDs(presenceBatch()), nil},
		{"already-present events, then new ones", append(presenceBatch(), fresh...),
			eventIDs(presenceBatch()), eventIDs(fresh)},
		{"same payload with other key order and spacing", respaced, eventIDs(presenceBatch()), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := openTestPool(t)
			ctx := context.Background()
			require.NoError(t, pool.Migrate(ctx))
			first, err := pool.PushEventsResult(ctx, presenceBatch())
			require.NoError(t, err)
			require.Equal(t, eventIDs(presenceBatch()), first.Inserted)

			res, err := pool.PushEventsResult(ctx, tt.repush)
			require.NoError(t, err)
			require.Equal(t, tt.wantPresent, res.AlreadyPresent, "each event already on the hub is acknowledged")
			require.Equal(t, tt.wantInserted, res.Inserted)
			require.Empty(t, res.Mismatches)
			require.Empty(t, res.Renumbers, "a re-pushed creation is never a renumber")
			require.Empty(t, res.Collisions)
			require.ElementsMatch(t, eventIDs(tt.repush), res.Accepted())
			require.Equal(t, len(tt.repush), hubRows(t, pool, eventIDs(tt.repush)), "no row is written twice")

			legacy, _, renumbers, err := pool.PushEventsWithRenumbers(ctx, tt.repush)
			require.NoError(t, err)
			require.ElementsMatch(t, eventIDs(tt.repush), legacy, "the legacy view reports them accepted too")
			require.Empty(t, renumbers)
		})
	}
}

// TestPushEvents_PresentWithOtherContent_NotAcknowledged: an event whose id
// the hub already holds with another node, op or payload is not
// acknowledged: it is reported as a mismatch naming what differs and the
// hub copy's node and op, the hub row is left as it was, and the legacy
// views do not report it accepted (ADR-006 I6; orchestrator decision Q4).
func TestPushEvents_PresentWithOtherContent_NotAcknowledged(t *testing.T) {
	const id = "0193fa00-0000-7000-8000-000000095302"
	tests := []struct {
		name       string
		node       string
		op         model.OpType
		payload    string
		wantFields []string
	}{
		{"another node", "MTIX-2", model.OpUpdateField, `{"field_name":"title","new_value":"renamed"}`,
			[]string{"node_id"}},
		{"another op", "MTIX-1", model.OpComment, `{"field_name":"title","new_value":"renamed"}`,
			[]string{"op_type"}},
		{"another payload", "MTIX-1", model.OpUpdateField, `{"field_name":"title","new_value":"other"}`,
			[]string{"payload"}},
		{"another node and payload", "MTIX-9", model.OpUpdateField, `{"field_name":"title"}`,
			[]string{"node_id", "payload"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := openTestPool(t)
			ctx := context.Background()
			require.NoError(t, pool.Migrate(ctx))
			_, err := pool.PushEventsResult(ctx, presenceBatch())
			require.NoError(t, err)

			changed := presenceEvent(id, tt.node, tt.op, tt.payload, 2)
			res, err := pool.PushEventsResult(ctx, []*model.SyncEvent{changed})
			require.NoError(t, err)
			require.Empty(t, res.Accepted(), "a copy with other content is never acknowledged")
			require.Equal(t, []transport.PresenceMismatch{{EventID: id, Fields: tt.wantFields,
				HubNodeID: "MTIX-1", HubOpType: string(model.OpUpdateField)}}, res.Mismatches)

			var node, op, payload string
			require.NoError(t, pool.Inner().QueryRow(ctx,
				`SELECT node_id, op_type, payload::text FROM sync_events WHERE event_id = $1`, id,
			).Scan(&node, &op, &payload))
			require.Equal(t, "MTIX-1", node, "the hub keeps its copy")
			require.Equal(t, string(model.OpUpdateField), op)
			require.JSONEq(t, `{"field_name":"title","new_value":"renamed"}`, payload)

			legacy, _, err := pool.PushEvents(ctx, []*model.SyncEvent{changed})
			require.NoError(t, err)
			require.Empty(t, legacy)
		})
	}
}

// TestPushEvents_LostCommitAck_RetryAcknowledges: the first COMMIT of a push
// succeeds on the hub but its acknowledgement is lost (the injected seam
// returns a connection error after the rows committed), so the transport's
// own retry runs the whole transaction again. The retry finds every event of
// the batch already on the hub and reports each one accepted, so none is
// left pending, and no row is written twice (MTIX-95.3 acceptance 6, review
// F-80).
func TestPushEvents_LostCommitAck_RetryAcknowledges(t *testing.T) {
	type pushFunc func(ctx context.Context, pool *transport.Pool, events []*model.SyncEvent) (
		present, inserted []string, err error)
	tests := []struct {
		name string
		push pushFunc
	}{
		{"result view", func(ctx context.Context, pool *transport.Pool, events []*model.SyncEvent) (
			[]string, []string, error,
		) {
			res, err := pool.PushEventsResult(ctx, events)
			return res.AlreadyPresent, res.Inserted, err
		}},
		{"legacy view the e2e harness uses", func(ctx context.Context, pool *transport.Pool, events []*model.SyncEvent) (
			[]string, []string, error,
		) {
			accepted, _, _, err := pool.PushEventsWithRenumbers(ctx, events)
			return accepted, nil, err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := openTestPool(t)
			ctx := context.Background()
			require.NoError(t, pool.Migrate(ctx))
			commits := 0
			transport.SetAfterCommitForTest(pool, func() error {
				commits++
				if commits == 1 {
					return &pgconn.PgError{Code: "08006", Message: "connection lost before the commit was acknowledged"}
				}
				return nil
			})

			events := presenceBatch()
			accepted, inserted, err := tt.push(ctx, pool, events)
			require.NoError(t, err)
			require.Equal(t, 2, commits, "the transport retried the whole transaction once")
			require.ElementsMatch(t, eventIDs(events), accepted,
				"every event of the batch is acknowledged as already on the hub, so none is left pending")
			require.Empty(t, inserted, "the retry inserted nothing: the lost commit did")
			require.Equal(t, len(events), hubRows(t, pool, eventIDs(events)))
		})
	}
}

// conflictingUpdates returns two concurrent title updates of MTIX-1 from
// two authors: pushed together, the second is recorded as a conflict with
// the first.
func conflictingUpdates() []*model.SyncEvent {
	a := presenceEvent("0193fa00-0000-7000-8000-000000095321", "MTIX-1", model.OpUpdateField,
		`{"field_name":"title","new_value":"from alice"}`, 1)
	b := presenceEvent("0193fa00-0000-7000-8000-000000095322", "MTIX-1", model.OpUpdateField,
		`{"field_name":"title","new_value":"from bob"}`, 1)
	b.AuthorID = "bob"
	b.VectorClock = model.VectorClock{"bob": 1}
	return []*model.SyncEvent{a, b}
}

// conflictRows returns how many sync_conflicts rows the hub holds.
func conflictRows(t *testing.T, pool *transport.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.Inner().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sync_conflicts`).Scan(&n))
	return n
}

// TestPushEvents_AlreadyPresent_ConflictsNotRecordedAgain: an event the
// push finds already on the hub is not checked for conflicts again. Its
// conflicts were recorded when it was inserted, so re-pushing it (after a
// lost acknowledgement, a crash before the local mark, or the transport's
// retry after a commit error) leaves sync_conflicts unchanged and reports
// no conflict, instead of recording each conflict once more per re-push
// (MTIX-95.3, orchestrator decision).
func TestPushEvents_AlreadyPresent_ConflictsNotRecordedAgain(t *testing.T) {
	tests := []struct {
		name     string
		lostAck  bool // the first push's COMMIT acknowledgement is lost
		repushed func(events []*model.SyncEvent) []*model.SyncEvent
	}{
		{"re-push the conflicting update", false,
			func(events []*model.SyncEvent) []*model.SyncEvent { return events[1:] }},
		{"re-push both updates", false,
			func(events []*model.SyncEvent) []*model.SyncEvent { return events }},
		{"the transport's retry after a lost commit acknowledgement", true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := openTestPool(t)
			ctx := context.Background()
			require.NoError(t, pool.Migrate(ctx))
			commits := 0
			if tt.lostAck {
				transport.SetAfterCommitForTest(pool, func() error {
					commits++
					if commits == 1 {
						return &pgconn.PgError{Code: "08006", Message: "connection lost before the commit was acknowledged"}
					}
					return nil
				})
			}

			events := conflictingUpdates()
			first, err := pool.PushEventsResult(ctx, events)
			require.NoError(t, err)
			require.Equal(t, 1, conflictRows(t, pool), "the concurrent update is recorded as one conflict")
			if tt.lostAck {
				require.Equal(t, 2, commits, "the transport retried the whole transaction once")
				require.ElementsMatch(t, eventIDs(events), first.AlreadyPresent)
				require.Empty(t, first.Conflicts, "the retry does not detect the conflicts again")
				return
			}
			require.Len(t, first.Conflicts, 1)

			repush := tt.repushed(events)
			res, err := pool.PushEventsResult(ctx, repush)
			require.NoError(t, err)
			require.ElementsMatch(t, eventIDs(repush), res.AlreadyPresent)
			require.Empty(t, res.Conflicts, "an event already on the hub reports no conflict again")
			require.Equal(t, 1, conflictRows(t, pool), "sync_conflicts is unchanged")
		})
	}
}
