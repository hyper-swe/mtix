// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// PG-free tests of the per-event push validation (MTIX-95.12, review F-15).
// Push used to send a batch whose every event the transport validated
// all-or-nothing (FR-18.7), so one event that is legal locally but over the
// 64 KB wire cap (a prompt may be 100 KB) failed every batch, and because
// the queue head never changed that replica stopped pushing for good. Push
// now validates each pending event, holds an invalid one in the local
// sync_quarantine table with source push and the reason, and pushes the
// rest; held events are left out of the pending reads.

// fakePushHub stands in for the hub in pushLoop tests. Like
// transport.PushEventsWithCollisions it validates the whole batch with
// validator.ValidateBatch before anything else and refuses the batch
// atomically on the first invalid event (FR-18.7); otherwise it accepts
// every event, except that a create_node listed in renumberOnce is refused
// once with RenumberRequired, as the hub does when another replica holds
// its number, and the first failCalls calls fail as an unreachable hub
// would. beforeCall, when set, runs at the start of every call, as another
// process working on the same database while the push runs would. It
// records the ids of every batch it was sent, and keeps the
// accepted events so a test can pull them into another replica.
type fakePushHub struct {
	calls        [][]string
	accepted     map[string]bool
	events       []*model.SyncEvent
	renumberOnce map[string]bool
	failCalls    int
	beforeCall   func(call int)
}

// newFakePushHub returns a hub that has received nothing.
func newFakePushHub() *fakePushHub {
	return &fakePushHub{accepted: map[string]bool{}}
}

// PushEventsWithRenumbers implements eventPusher.
func (h *fakePushHub) PushEventsWithRenumbers(_ context.Context, events []*model.SyncEvent) (
	[]string, []transport.ConflictDescriptor, []transport.RenumberRequired, error,
) {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.EventID)
	}
	h.calls = append(h.calls, ids)
	if h.beforeCall != nil {
		h.beforeCall(len(h.calls))
	}
	if h.failCalls > 0 {
		h.failCalls--
		return nil, nil, nil, fmt.Errorf("PushEvents: hub unreachable")
	}
	if err := validator.ValidateBatch(events, time.Now().UTC(), nil); err != nil {
		return nil, nil, nil, fmt.Errorf("PushEvents validate: %w", err)
	}
	var accepted []string
	var renumbers []transport.RenumberRequired
	for _, e := range events {
		if h.renumberOnce[e.EventID] {
			delete(h.renumberOnce, e.EventID)
			renumbers = append(renumbers, transport.RenumberRequired{
				EventID: e.EventID, ProjectPrefix: e.ProjectPrefix, DisplayPath: e.NodeID})
			continue
		}
		h.accepted[e.EventID] = true
		h.events = append(h.events, e)
		accepted = append(accepted, e.EventID)
	}
	return accepted, nil, renumbers, nil
}

// sent reports whether any batch sent to the hub carried eventID.
func (h *fakePushHub) sent(eventID string) bool {
	for _, call := range h.calls {
		for _, id := range call {
			if id == eventID {
				return true
			}
		}
	}
	return false
}

// overWireCap is a field value that is legal locally (prompts may be
// 100 KB) but makes its event's payload larger than the 64 KB wire cap.
func overWireCap() string {
	return strings.Repeat("p", validator.MaxPayloadBytes+4*1024)
}

// eventIDFor returns the id of the local event of op for nodeID.
func eventIDFor(t *testing.T, nodeID string, op model.OpType) string {
	t.Helper()
	var id string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT event_id FROM sync_events WHERE node_id = ? AND op_type = ? ORDER BY lamport_clock DESC LIMIT 1`,
		nodeID, string(op)).Scan(&id))
	return id
}

// syncStatusOf returns the sync_status of the local event eventID.
func syncStatusOf(t *testing.T, eventID string) string {
	t.Helper()
	var s string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT sync_status FROM sync_events WHERE event_id = ?`, eventID).Scan(&s))
	return s
}

// stampInFuture moves eventID's wall clock two days ahead, past the FR-18.8
// grace the hub allows.
func stampInFuture(t *testing.T, eventID string) {
	t.Helper()
	_, err := app.store.WriteDB().ExecContext(context.Background(),
		`UPDATE sync_events SET wall_clock_ts = ? WHERE event_id = ?`,
		time.Now().Add(48*time.Hour).UnixMilli(), eventID)
	require.NoError(t, err)
}

// stampAt sets eventID's wall clock to at.
func stampAt(t *testing.T, eventID string, at time.Time) {
	t.Helper()
	_, err := app.store.WriteDB().ExecContext(context.Background(),
		`UPDATE sync_events SET wall_clock_ts = ? WHERE event_id = ?`, at.UnixMilli(), eventID)
	require.NoError(t, err)
}

// requireHeldDependent asserts the latest event of op for node is held with
// source push as a dependent of the held creation rootEventID, and was
// never sent to hub.
func requireHeldDependent(t *testing.T, hub *fakePushHub, node string, op model.OpType, rootEventID string) {
	t.Helper()
	id := eventIDFor(t, node, op)
	q, ok := quarantined(t)[id]
	require.True(t, ok, "%s %s is held", node, op)
	require.Equal(t, "push", q.Source)
	require.True(t, strings.HasPrefix(q.Reason, "depends on held create of "+rootEventID), q.Reason)
	require.False(t, hub.sent(id), "%s %s is never sent", node, op)
	require.Equal(t, "pending", syncStatusOf(t, id))
}

// TestPushLoop_OversizedEvent_HeldOthersPushed: an invalid pending event is
// held in sync_quarantine with source push and the reason, wherever it sits
// in the batch, the rest of the batch is pushed, and the held event stays in
// sync_events as pending (MTIX-95.12 acceptance 1).
func TestPushLoop_OversizedEvent_HeldOthersPushed(t *testing.T) {
	tests := []struct {
		name       string
		total, bad int
		oversized  bool // the bad node has a prompt over the wire cap
		future     bool // the bad node's event is stamped 2 days ahead
		wantReason []string
	}{
		{"oversized first of three", 3, 0, true, false, []string{"payload", "65536", "prompt"}},
		{"oversized middle of three", 3, 1, true, false, []string{"payload", "65536", "prompt"}},
		{"oversized last of three", 3, 2, true, false, []string{"payload", "65536", "prompt"}},
		{"oversized only event", 1, 0, true, false, []string{"payload", "65536", "prompt"}},
		{"stamped too far in the future", 3, 1, false, true, []string{"future"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ids := make([]string, tt.total)
			for i := range ids {
				prompt := ""
				if i == tt.bad && tt.oversized {
					prompt = overWireCap()
				}
				require.NoError(t, runCreate(fmt.Sprintf("node %d", i), "", "", 3, "", prompt, "", "", ""))
				ids[i] = eventIDFor(t, fmt.Sprintf("TEST-%d", i+1), model.OpCreateNode)
			}
			if tt.future {
				stampInFuture(t, ids[tt.bad])
			}

			hub := newFakePushHub()
			var stderr bytes.Buffer
			pushed, _, _, _, err := pushLoop(context.Background(), &stderr, hub, app.store)
			require.NoError(t, err, "one invalid event must not fail the push")
			require.Equal(t, tt.total-1, pushed)

			for i, id := range ids {
				if i == tt.bad {
					require.False(t, hub.sent(id), "the invalid event is never sent to the hub")
					require.Equal(t, "pending", syncStatusOf(t, id), "a held event is kept, never deleted")
					continue
				}
				require.True(t, hub.accepted[id], "event %d is pushed", i)
				require.Equal(t, "pushed", syncStatusOf(t, id))
			}
			q, ok := quarantined(t)[ids[tt.bad]]
			require.True(t, ok, "the invalid event is held in sync_quarantine")
			require.Equal(t, "push", q.Source)
			require.Equal(t, 1, q.Attempts)
			for _, want := range tt.wantReason {
				require.Contains(t, strings.ToLower(q.Reason), want)
			}
			require.Contains(t, stderr.String(), ids[tt.bad], "push names the held event")
		})
	}
}

// duplicateHeldEvents copies eventID n times as further pending events,
// each one Lamport tick later, and moves the local Lamport clock past them
// so later mutations sort after them.
func duplicateHeldEvents(t *testing.T, eventID string, n int) {
	t.Helper()
	ctx := context.Background()
	var base int64
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT lamport_clock FROM sync_events WHERE event_id = ?`, eventID).Scan(&base))
	for i := 1; i <= n; i++ {
		_, err := app.store.WriteDB().ExecContext(ctx, `
			INSERT INTO sync_events (event_id, project_prefix, node_id, uid, op_type, payload,
			  wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash,
			  sync_status, created_at)
			SELECT ?, project_prefix, node_id, uid, op_type, payload, wall_clock_ts,
			  lamport_clock + ?, vector_clock, author_id, author_machine_hash, sync_status, created_at
			FROM sync_events WHERE event_id = ?`,
			fmt.Sprintf("%s-copy-%03d", eventID, i), i, eventID)
		require.NoError(t, err)
	}
	_, err := app.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.lamport'`, fmt.Sprint(base+int64(n)))
	require.NoError(t, err)
}

// TestPushLoop_HeldEventsDoNotWedgeQueue: held events are left out of the
// pending reads, so however many sit at the head of the queue (a full batch
// of them included), the valid event behind them pushes in the same run, a
// later valid event pushes in the next run, the held events are not sent or
// checked again, and readPendingBatch no longer returns them (MTIX-95.12
// acceptance 2).
func TestPushLoop_HeldEventsDoNotWedgeQueue(t *testing.T) {
	tests := []struct {
		name string
		held int
	}{
		{"one held event", 1},
		{"a full batch of held events", pushBatchSize},
		{"more than a batch of held events", pushBatchSize + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			require.NoError(t, runCreate("big", "", "", 3, "", overWireCap(), "", "", ""))
			duplicateHeldEvents(t, eventIDFor(t, "TEST-1", model.OpCreateNode), tt.held-1)
			require.NoError(t, runCreate("behind the held events", "", "", 3, "", "", "", "", ""))
			behind := eventIDFor(t, "TEST-2", model.OpCreateNode)

			hub := newFakePushHub()
			var stderr bytes.Buffer
			pushed, _, _, _, err := pushLoop(ctx, &stderr, hub, app.store)
			require.NoError(t, err)
			require.Equal(t, 1, pushed, "the valid event behind the held ones is pushed")
			require.True(t, hub.accepted[behind])
			require.Len(t, hub.accepted, 1, "no held event reaches the hub")
			require.Equal(t, tt.held, countTestRows(t,
				`SELECT COUNT(*) FROM sync_quarantine WHERE source = 'push'`))

			rest, err := readPendingBatch(ctx, app.store, pushBatchSize)
			require.NoError(t, err)
			require.Empty(t, rest, "held events are not pending reads")

			require.NoError(t, runCreate("later", "", "", 3, "", "", "", "", ""))
			later := eventIDFor(t, "TEST-3", model.OpCreateNode)
			second := newFakePushHub()
			pushed, _, _, _, err = pushLoop(ctx, &stderr, second, app.store)
			require.NoError(t, err)
			require.Equal(t, 1, pushed, "a later valid event still pushes")
			require.Equal(t, [][]string{{later}}, second.calls, "held events are not sent again")
			require.Equal(t, "pushed", syncStatusOf(t, later))
			require.Equal(t, 0, countTestRows(t,
				`SELECT COUNT(*) FROM sync_quarantine WHERE source = 'push' AND attempts <> 1
				 AND substr(reason, 1, 26) <> 'depends on held create of '`),
				"a permanent hold is not checked again")
			require.Equal(t, 0, countTestRows(t,
				`SELECT COUNT(*) FROM sync_quarantine WHERE source = 'push' AND attempts <> 2
				 AND substr(reason, 1, 26) = 'depends on held create of '`),
				"a dependent counts the push that checked it against its held creation")
			require.Equal(t, tt.held, countTestRows(t,
				`SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`), "held events stay pending")
		})
	}
}
