// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// The tuple pull cursor (MTIX-95.4; ADR-006 D6): the cursor pass pages by
// (lamport_clock, event_id), saves both halves of its cursor in the
// transaction that applies the batch, and a client upgraded from a
// Lamport-only cursor reads the events at exactly its clock again,
// idempotently. The fake hub is fakeLateHub (sync_pull_sweep_test.go),
// whose cursor pass serves the same keyset as the hub; the real-Postgres
// paths are in sync_pull_cursor_pg_test.go.

// remoteCreateAt builds a well-formed create_node event for root node
// nodeID, by another identity, stamped with lamport. Its event id is a new
// UUIDv7 that this store's log does not hold, so a pull applies it.
func remoteCreateAt(t *testing.T, nodeID string, lamport int64) *model.SyncEvent {
	t.Helper()
	eid, err := clock.NewEventID()
	require.NoError(t, err)
	payload, err := model.EncodePayload(model.CreateNodePayload{
		Title:    "remote " + nodeID,
		NodeType: model.NodeTypeForDepth(0),
		Priority: model.PriorityMedium,
		Creator:  "remote-author",
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	return &model.SyncEvent{
		EventID:           eid,
		ProjectPrefix:     "TEST",
		NodeID:            nodeID,
		UID:               eid,
		OpType:            model.OpCreateNode,
		Payload:           payload,
		WallClockTS:       now.UnixMilli(),
		LamportClock:      lamport,
		VectorClock:       model.VectorClock{"remote-author": lamport},
		AuthorID:          "remote-author",
		AuthorMachineHash: "ffffffffffffffff",
		CreatedAt:         now,
	}
}

// remoteCreates returns one remote create per Lamport clock in lamports, in
// the hub's keyset order (lamport_clock, then event id), creating TEST-1,
// TEST-2, ... in that order.
func remoteCreates(t *testing.T, lamports ...int64) []*model.SyncEvent {
	t.Helper()
	ids := make([]string, len(lamports))
	for i := range ids {
		id, err := clock.NewEventID()
		require.NoError(t, err)
		ids[i] = id
	}
	sort.Strings(ids)
	sorted := append([]int64(nil), lamports...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	out := make([]*model.SyncEvent, len(sorted))
	for i, l := range sorted {
		e := remoteCreateAt(t, fmt.Sprintf("TEST-%d", i+1), l)
		e.EventID, e.UID = ids[i], ids[i]
		out[i] = e
	}
	return out
}

// requireSavedCursor asserts the peer's saved pull cursor, both halves.
func requireSavedCursor(t *testing.T, want transport.PullCursor) {
	t.Helper()
	got, err := readLastPulledClock(context.Background(), app.store)
	require.NoError(t, err)
	require.Equal(t, want, got, "saved pull cursor")
}

// requireAppliedOnce asserts the peer applied e: one applied_events row,
// one mirror row in sync_events, and its node, with the title e carries.
func requireAppliedOnce(t *testing.T, e *model.SyncEvent) {
	t.Helper()
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, e.EventID),
		"%s (%s) is applied", e.EventID, e.NodeID)
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE event_id = ?`, e.EventID))
	var p model.CreateNodePayload
	require.NoError(t, json.Unmarshal(e.Payload, &p))
	node, err := app.store.GetNode(context.Background(), e.NodeID)
	require.NoError(t, err)
	require.Equal(t, p.Title, node.Title)
}

// execPeer runs statements, which take no arguments, on the peer store's
// write connection.
func execPeer(t *testing.T, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		_, err := app.store.WriteDB().ExecContext(context.Background(), stmt)
		require.NoError(t, err, stmt)
	}
}

// setPeerMeta sets one meta key on the peer store, adding the row when it
// is absent.
func setPeerMeta(t *testing.T, key, value string) {
	t.Helper()
	_, err := app.store.WriteDB().ExecContext(context.Background(),
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	require.NoError(t, err)
}

// crashAtPullCursorWrite makes every write of either pull cursor key on the
// peer store fail, as a crash at that write would, until the returned
// function removes the triggers.
func crashAtPullCursorWrite(t *testing.T) (restore func()) {
	t.Helper()
	execPeer(t,
		`CREATE TRIGGER test_crash_at_cursor_update BEFORE UPDATE ON meta
		 WHEN NEW.key IN ('meta.sync.last_pulled_clock', 'meta.sync.last_pulled_event_id')
		 BEGIN SELECT RAISE(ABORT, 'simulated crash at the pull cursor write'); END`,
		`CREATE TRIGGER test_crash_at_cursor_insert BEFORE INSERT ON meta
		 WHEN NEW.key IN ('meta.sync.last_pulled_clock', 'meta.sync.last_pulled_event_id')
		 BEGIN SELECT RAISE(ABORT, 'simulated crash at the pull cursor write'); END`)
	return func() {
		execPeer(t, `DROP TRIGGER test_crash_at_cursor_update`, `DROP TRIGGER test_crash_at_cursor_insert`)
	}
}

// TestPullLoop_EqualLamportAcrossBatches_AllAppliedOnce: the cursor pass
// with --limit 2 over three events that share one Lamport clock applies all
// three, asks for the second page after the first page's last event (its
// clock and its event id) and saves the cursor at the third event. Before
// MTIX-95.4 the second page asked for clocks above the tie, so the third
// event was never pulled.
func TestPullLoop_EqualLamportAcrossBatches_AllAppliedOnce(t *testing.T) {
	initTestApp(t)
	events := remoteCreates(t, 5, 5, 5)
	hub := &fakeLateHub{pullEvents: events}

	applied, batches, err := pullLoop(context.Background(), testIngest(nil), hub, app.store,
		transport.PullCursor{}, 2)

	require.NoError(t, err)
	require.Equal(t, 3, applied)
	require.Equal(t, 2, batches)
	require.Equal(t, []transport.PullCursor{{}, transport.CursorAt(events[1])}, hub.pullCursors)
	for _, e := range events {
		requireAppliedOnce(t, e)
	}
	requireSavedCursor(t, transport.CursorAt(events[2]))
}

// TestPullLoop_CursorWrittenWithBatchApply: the pull cursor is saved in the
// transaction that applies the batch, so a crash between the apply and the
// cursor write cannot happen: when the cursor write fails, the batch's
// applies and quarantine rows roll back with it, and the next pull applies
// the batch and saves the cursor past it, a quarantined event included.
// Before MTIX-95.4 the batch committed first and the cursor was written in
// a second transaction.
func TestPullLoop_CursorWrittenWithBatchApply(t *testing.T) {
	tests := []struct {
		name       string
		quarantine bool
	}{
		{"every event applies", false},
		{"the middle event is quarantined", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			events := offlineEvents(t)
			if tt.quarantine {
				bad := *events[1]
				bad.Payload = json.RawMessage(`{"field_name":"not_a_field","new_value":"x"}`)
				events[1] = &bad
			}
			hub := &fakeLateHub{pullEvents: events}
			restore := crashAtPullCursorWrite(t)

			_, _, err := pullLoop(ctx, testIngest(nil), hub, app.store, transport.PullCursor{}, 100)

			require.ErrorContains(t, err, "simulated crash at the pull cursor write")
			for _, e := range events {
				requireNotApplied(t, e)
			}
			require.Empty(t, quarantined(t), "the quarantine write rolls back with the batch")
			require.Zero(t, peerLamport(t))
			requireSavedCursor(t, transport.PullCursor{})

			restore()
			applied, _, err := pullLoop(ctx, testIngest(nil), hub, app.store, transport.PullCursor{}, 100)

			require.NoError(t, err)
			requireSavedCursor(t, transport.CursorAt(events[2]))
			if !tt.quarantine {
				require.Equal(t, 3, applied)
				require.Empty(t, quarantined(t))
				return
			}
			require.Equal(t, 2, applied)
			requireQuarantinedAs(t, events[1], "pull", "not_a_field")
		})
	}
}

// TestApplyPullBatch_CursorPassSavesCursorInBatchTransaction: a batch of the
// cursor pass (source pull) saves the pull cursor, both halves, at its last
// event whose Lamport clock is accepted against the local clock after the
// batch: past a quarantined event, never to a refused clock. A batch of the
// late-event sweep (source sweep) leaves the cursor alone.
func TestApplyPullBatch_CursorPassSavesCursorInBatchTransaction(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		mutate  func(last *model.SyncEvent)
		wantIdx int // index of the event the cursor is saved at; -1 for unchanged
	}{
		{"cursor pass, every event applies", quarantineSourcePull, nil, 2},
		{"cursor pass, the last event is quarantined", quarantineSourcePull, func(e *model.SyncEvent) {
			e.Payload = json.RawMessage(`{"field_name":"not_a_field","new_value":"x"}`)
		}, 2},
		{"cursor pass, the last event's clock is refused", quarantineSourcePull, func(e *model.SyncEvent) {
			e.LamportClock = 1 << 40
		}, 1},
		{"late-event sweep", quarantineSourceSweep, nil, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			events := offlineEvents(t)
			if tt.mutate != nil {
				last := *events[2]
				tt.mutate(&last)
				events[2] = &last
			}

			_, err := applyPullBatch(context.Background(), testIngest(nil), app.store, tt.source, events)

			require.NoError(t, err)
			if tt.wantIdx < 0 {
				requireSavedCursor(t, transport.PullCursor{})
				return
			}
			requireSavedCursor(t, transport.CursorAt(events[tt.wantIdx]))
		})
	}
}

// TestApplyPullBatch_EveryClockRefused_CursorUnchanged: a cursor-pass batch
// whose every event is refused for its Lamport clock quarantines them and
// leaves the saved cursor where it was.
func TestApplyPullBatch_EveryClockRefused_CursorUnchanged(t *testing.T) {
	initTestApp(t)
	saved := transport.PullCursor{Lamport: 3, EventID: "0193fb00-0000-7000-8000-000000000003"}
	setPeerMeta(t, "meta.sync.last_pulled_clock", "3")
	setPeerMeta(t, "meta.sync.last_pulled_event_id", saved.EventID)
	extreme := *offlineEvents(t)[0]
	extreme.LamportClock = 1 << 40

	held, err := applyPullBatch(context.Background(), testIngest(nil), app.store, quarantineSourcePull,
		[]*model.SyncEvent{&extreme})

	require.NoError(t, err)
	require.Len(t, held, 1)
	requireSavedCursor(t, saved)
}

// TestReadLastPulledClock_LamportOnlyCursor_ReReadsBoundaryIdempotently: a
// store upgraded from a Lamport-only cursor has no event id saved (the key
// is absent until the store is opened by this version, which seeds it
// empty). readLastPulledClock returns the cursor with an empty event id,
// which sorts before every event at the saved clock, so the cursor pass
// reads the events at exactly that clock again: the ones already applied
// are deduplicated (no second apply, no conflict, nodes unchanged), and one
// the old paging skipped is applied.
func TestReadLastPulledClock_LamportOnlyCursor_ReReadsBoundaryIdempotently(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"event-id key absent (written before the upgrade)", func(t *testing.T) {
			execPeer(t, `DELETE FROM meta WHERE key = 'meta.sync.last_pulled_event_id'`)
		}},
		{"event-id key seeded empty", func(t *testing.T) {
			setPeerMeta(t, "meta.sync.last_pulled_event_id", "")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			events := remoteCreates(t, 5, 5, 5, 6)
			// The pull before the upgrade applied the first two events at 5
			// and saved the Lamport-only cursor 5: the third event at 5 did
			// not fit on the page and was skipped.
			held, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourceSweep, events[:2])
			require.NoError(t, err)
			require.Empty(t, held)
			setPeerMeta(t, "meta.sync.last_pulled_clock", "5")
			tt.setup(t)
			before := nodeSnapshot(t, events[0].NodeID, events[1].NodeID)

			cursor, err := readLastPulledClock(ctx, app.store)
			require.NoError(t, err)
			require.Equal(t, transport.PullCursor{Lamport: 5}, cursor)

			hub := &fakeLateHub{pullEvents: events}
			_, _, err = pullLoop(ctx, testIngest(nil), hub, app.store, cursor, 2)

			require.NoError(t, err)
			require.Equal(t, cursor, hub.pullCursors[0], "the first page starts at the saved clock")
			for _, e := range events {
				requireAppliedOnce(t, e)
			}
			require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_conflicts`), "the re-read logs no conflict")
			require.Equal(t, before, nodeSnapshot(t, events[0].NodeID, events[1].NodeID),
				"the events read again change nothing")
			requireSavedCursor(t, transport.CursorAt(events[3]))
		})
	}
}

// nodeSnapshot returns, per node id, the columns an apply would change.
func nodeSnapshot(t *testing.T, ids ...string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		var title, status, updatedAt, hash string
		require.NoError(t, app.store.QueryRow(context.Background(),
			`SELECT title, status, updated_at, content_hash FROM nodes WHERE id = ?`, id,
		).Scan(&title, &status, &updatedAt, &hash))
		out[id] = title + "|" + status + "|" + updatedAt + "|" + hash
	}
	return out
}

// TestReadLastPulledClock_ReturnsSavedTuple: the cursor is read back as
// saved, both halves; a Lamport half that is negative or not a number is
// refused as corrupted state.
func TestReadLastPulledClock_ReturnsSavedTuple(t *testing.T) {
	tests := []struct {
		name, clock, eventID string
		want                 transport.PullCursor
		wantErr              string
	}{
		{"fresh store", "0", "", transport.PullCursor{}, ""},
		{"saved tuple", "42", "0193fb00-0000-7000-8000-000000000042",
			transport.PullCursor{Lamport: 42, EventID: "0193fb00-0000-7000-8000-000000000042"}, ""},
		{"negative clock", "-1", "", transport.PullCursor{}, "negative"},
		{"clock not a number", "x", "", transport.PullCursor{}, "parse cursor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			setPeerMeta(t, "meta.sync.last_pulled_clock", tt.clock)
			setPeerMeta(t, "meta.sync.last_pulled_event_id", tt.eventID)

			got, err := readLastPulledClock(context.Background(), app.store)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
