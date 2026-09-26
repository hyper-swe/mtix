// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Tests of push acknowledgement by presence (MTIX-95.3, ADR-006 D7/D48).
// An event that reached the hub but was never marked pushed locally (a
// lost commit acknowledgement, a crash or disk failure before the local
// mark, a retry after a network drop) used to stay pending forever: the hub
// reported it accepted only when its INSERT added a row, and once a full
// batch of such events sat at the head of the queue, push stopped at that
// batch and reported success while nothing behind it was ever sent. Each
// test runs against the fake hub and, when MTIX_PG_TEST_DSN is set, against
// a real hub through transport.Pool.

// pushLoopCounts runs pushLoop and returns its totals in the order older
// tests read them: the events marked pushed (inserted and already on the
// hub), the batches sent, the conflicts and the renumbers.
func pushLoopCounts(ctx context.Context, stderr io.Writer, pool eventPusher, store *sqlite.Store,
) (pushed, batches, conflicts, renumbered int, err error) {
	tot, err := pushLoop(ctx, stderr, pool, store)
	return tot.pushed(), tot.batches, tot.conflicts, tot.renumbered, err
}

// ackHubCase opens one hub for a push acknowledgement test: the hub, and a
// probe that reports whether the hub holds an event.
type ackHubCase struct {
	name string
	open func(t *testing.T) (eventPusher, func(eventID string) bool)
}

// ackHubCases returns the fake hub and a real one (skipped when
// MTIX_PG_TEST_DSN is unset, see openCmdHub).
func ackHubCases() []ackHubCase {
	return []ackHubCase{
		{"fake hub", func(_ *testing.T) (eventPusher, func(string) bool) {
			hub := newFakePushHub()
			return hub, func(id string) bool { return hub.held(id) != nil }
		}},
		{"real hub", func(t *testing.T) (eventPusher, func(string) bool) {
			pool := openCmdHub(t)
			return pool, func(id string) bool {
				var n int
				require.NoError(t, pool.Inner().QueryRow(context.Background(),
					`SELECT COUNT(*) FROM sync_events WHERE event_id = $1`, id).Scan(&n))
				return n == 1
			}
		}},
	}
}

// createTasks creates n tasks and returns their creation events' ids.
func createTasks(t *testing.T, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		require.NoError(t, runCreate(fmt.Sprintf("task %d", i), "", "", 3, "", "", "", "", ""))
		var id string
		require.NoError(t, app.store.QueryRow(context.Background(),
			`SELECT event_id FROM sync_events WHERE op_type = 'create_node'
			 ORDER BY lamport_clock DESC LIMIT 1`).Scan(&id))
		ids = append(ids, id)
	}
	return ids
}

// cloneCreates adds n pending creation events after eventID's, each a copy
// of it for another task number (TEST-1001, TEST-1002, ...), with its own
// id as uid and one Lamport tick after the previous one, and moves the
// local Lamport clock past them so later changes sort after them. It
// returns the copies' ids.
func cloneCreates(t *testing.T, eventID string, n int) []string {
	t.Helper()
	ctx := context.Background()
	var base int64
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT lamport_clock FROM sync_events WHERE event_id = ?`, eventID).Scan(&base))
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("0193fa00-0000-7000-8000-%012d", i)
		_, err := app.store.WriteDB().ExecContext(ctx, `
			INSERT INTO sync_events (event_id, project_prefix, node_id, uid, op_type, payload,
			  wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash,
			  sync_status, created_at)
			SELECT ?, project_prefix, ?, ?, op_type, payload, wall_clock_ts,
			  lamport_clock + ?, vector_clock, author_id, author_machine_hash, sync_status, created_at
			FROM sync_events WHERE event_id = ?`,
			id, fmt.Sprintf("TEST-%d", 1000+i), id, i, eventID)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	_, err := app.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.lamport'`, fmt.Sprint(base+int64(n)))
	require.NoError(t, err)
	return ids
}

// pendingCount returns how many local events are pending.
func pendingCount(t *testing.T) int {
	t.Helper()
	return countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`)
}

// putOnHub sends the pending events ids straight to hub, without marking
// them pushed: an earlier push whose hub commit landed but whose local mark
// was lost.
func putOnHub(t *testing.T, hub eventPusher, ids []string) {
	t.Helper()
	ctx := context.Background()
	events, err := app.store.ReadPendingEventsByID(ctx, ids)
	require.NoError(t, err)
	require.Len(t, events, len(ids))
	res, err := hub.PushEventsResult(ctx, events)
	require.NoError(t, err)
	require.Len(t, res.Inserted, len(ids))
}

// TestPushLoop_MarkPushedFailsAfterHubCommit_NextPushAcks: the hub commits a
// batch, then marking it pushed locally fails (a crash or a full disk). The
// next push reports the events already on the hub accepted, marks them
// pushed, and pushes the events queued after them (MTIX-95.3 acceptance 2).
func TestPushLoop_MarkPushedFailsAfterHubCommit_NextPushAcks(t *testing.T) {
	for _, hc := range ackHubCases() {
		t.Run(hc.name, func(t *testing.T) {
			hub, onHub := hc.open(t)
			initTestApp(t)
			ctx := context.Background()
			first := createTasks(t, 3)

			var marks int
			failing := context.WithValue(ctx, markPushedFailKey{}, func(ids []string) error {
				marks++
				require.ElementsMatch(t, first, ids, "the batch the hub committed")
				return errors.New("disk I/O error")
			})
			var stderr bytes.Buffer
			_, err := pushLoop(failing, &stderr, hub, app.store)
			require.ErrorContains(t, err, "mark pushed batch 1")
			require.Equal(t, 1, marks)
			for _, id := range first {
				require.True(t, onHub(id), "the hub committed the batch")
				require.Equal(t, "pending", syncStatusOf(t, id), "the local mark was lost")
			}

			later := createTasks(t, 2)
			stderr.Reset()
			tot, err := pushLoop(ctx, &stderr, hub, app.store)
			require.NoError(t, err, stderr.String())
			require.Equal(t, len(first), tot.present, "the events already on the hub are accepted")
			require.Equal(t, len(later), tot.inserted, "the later events are pushed")
			for _, id := range append(append([]string{}, first...), later...) {
				require.Equal(t, "pushed", syncStatusOf(t, id))
				require.True(t, onHub(id))
			}
			require.Zero(t, pendingCount(t))
		})
	}
}

// TestPushLoop_HeadOfQueueAlreadyOnHub_Drains: 150 events already on the
// hub (more than one 100-event batch) sit at the head of the pending queue,
// followed by new events. One push marks all 150 pushed and pushes every
// later event, and the pending count reaches 0; before, the first batch was
// acknowledged by nothing, push stopped there and reported success with the
// queue wedged (MTIX-95.3 acceptance 3).
func TestPushLoop_HeadOfQueueAlreadyOnHub_Drains(t *testing.T) {
	const alreadyOnHub = pushBatchSize + pushBatchSize/2
	for _, hc := range ackHubCases() {
		t.Run(hc.name, func(t *testing.T) {
			hub, onHub := hc.open(t)
			initTestApp(t)
			ctx := context.Background()
			head := createTasks(t, 1)
			head = append(head, cloneCreates(t, head[0], alreadyOnHub-1)...)
			putOnHub(t, hub, head)
			later := createTasks(t, 3)
			require.Equal(t, alreadyOnHub+len(later), pendingCount(t))

			var stderr bytes.Buffer
			tot, err := pushLoop(ctx, &stderr, hub, app.store)
			require.NoError(t, err, stderr.String())
			require.Equal(t, alreadyOnHub, tot.present, "every event already on the hub is acknowledged")
			require.Equal(t, len(later), tot.inserted, "every later event is pushed")
			require.Equal(t, 2, tot.batches)
			require.Zero(t, pendingCount(t), "the queue drains")
			for _, id := range head {
				require.Equal(t, "pushed", syncStatusOf(t, id))
			}
			for _, id := range later {
				require.True(t, onHub(id))
				require.Equal(t, "pushed", syncStatusOf(t, id))
			}
		})
	}
}

// TestPushAndReport_InsertedAndAlreadyPresent_SeparateCounts pins the push
// output: each batch's progress line on stderr and the summary on stdout
// give the events inserted and the events already on the hub as separate
// counts (MTIX-95.3 acceptance 4).
func TestPushAndReport_InsertedAndAlreadyPresent_SeparateCounts(t *testing.T) {
	tests := []struct {
		name               string
		onHub, fresh       int
		wantStderr, wantOK string
	}{
		{"only new events", 0, 2,
			"push progress: batch 1 (2 sent, 2 accepted: 2 inserted, 0 already on the hub; 0 renumbered, 0 conflicts)\n",
			"push complete: 2 events pushed across 1 batches (2 inserted, 0 already on the hub); 0 renumbered, 0 conflicts surfaced\n"},
		{"only events already on the hub", 3, 0,
			"push progress: batch 1 (3 sent, 3 accepted: 0 inserted, 3 already on the hub; 0 renumbered, 0 conflicts)\n",
			"push complete: 3 events pushed across 1 batches (0 inserted, 3 already on the hub); 0 renumbered, 0 conflicts surfaced\n"},
		{"events already on the hub, then new ones", 3, 2,
			"push progress: batch 1 (5 sent, 5 accepted: 2 inserted, 3 already on the hub; 0 renumbered, 0 conflicts)\n",
			"push complete: 5 events pushed across 1 batches (2 inserted, 3 already on the hub); 0 renumbered, 0 conflicts surfaced\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			hub := newFakePushHub()
			putOnHub(t, hub, createTasks(t, tt.onHub))
			createTasks(t, tt.fresh)

			var stdout, stderr bytes.Buffer
			require.NoError(t, pushAndReport(context.Background(), &stdout, &stderr, hub, app.store))
			require.Equal(t, tt.wantStderr, stderr.String())
			require.Equal(t, tt.wantOK, stdout.String())
		})
	}
}

// TestPushLoop_IDOnHubWithOtherContent_HeldNotAcknowledged: an event whose
// id the hub holds with a different payload is never acknowledged (ADR-006
// I6; orchestrator decision Q4 for MTIX-95.3). Push holds it in
// sync_quarantine with source push and a permanent reason naming what
// differs, names it on stderr, keeps it pending, pushes the rest, and does
// not send it again; doctor reports it; the later changes of that task are
// held with it, since its creation is held.
func TestPushLoop_IDOnHubWithOtherContent_HeldNotAcknowledged(t *testing.T) {
	for _, hc := range ackHubCases() {
		t.Run(hc.name, func(t *testing.T) {
			hub, onHub := hc.open(t)
			initTestApp(t)
			ctx := context.Background()
			ids := createTasks(t, 2)
			events, err := app.store.ReadPendingEventsByID(ctx, ids[:1])
			require.NoError(t, err)
			other := *events[0]
			other.Payload = []byte(`{"title":"another task under this event id"}`)
			res, err := hub.PushEventsResult(ctx, []*model.SyncEvent{&other})
			require.NoError(t, err)
			require.Len(t, res.Inserted, 1)

			var stderr bytes.Buffer
			tot, err := pushLoop(ctx, &stderr, hub, app.store)
			require.NoError(t, err, stderr.String())
			require.Equal(t, 1, tot.inserted, "the other event is pushed")
			require.Zero(t, tot.present, "the event with other content is not acknowledged")
			require.Equal(t, 1, tot.mismatched)
			require.Equal(t, "pending", syncStatusOf(t, ids[0]))
			require.Equal(t, "pushed", syncStatusOf(t, ids[1]))
			require.True(t, onHub(ids[1]))
			q, ok := quarantined(t)[ids[0]]
			require.True(t, ok, "the event is held")
			require.Equal(t, "push", q.Source)
			require.True(t, strings.HasPrefix(q.Reason, holdRefusedPrefix+"the hub already holds this event id with a different payload"), q.Reason)
			require.Contains(t, q.Reason, "TEST-1 create_node")
			require.Contains(t, stderr.String(), "push: held event "+ids[0])
			pass, detail := checkHeldPushEvents(ctx, app.store)
			require.False(t, pass)
			require.Contains(t, detail, "the hub already holds this event id with a different payload")

			require.NoError(t, runUpdate("TEST-1", "retitled", "", "", "", 0, "", ""))
			stderr.Reset()
			tot, err = pushLoop(ctx, &stderr, hub, app.store)
			require.NoError(t, err, stderr.String())
			require.Zero(t, tot.pushed(), "neither the held creation nor its later change is sent")
			retitle := eventIDFor(t, "TEST-1", model.OpUpdateField)
			require.True(t, strings.HasPrefix(quarantined(t)[retitle].Reason, holdDependsPrefix+ids[0]),
				quarantined(t)[retitle].Reason)
			require.False(t, onHub(retitle))
		})
	}
}

// TestPushLoop_IDOnHubWithOtherContent_LaterBatchChangesHeld: when a task's
// creation is held in one batch because the hub holds its id with other
// content, a change of that task in a later batch of the same push is held
// with it as a dependent and never sent (MTIX-95.3): the held creation
// joins the push's held creations before the next batch is decided.
func TestPushLoop_IDOnHubWithOtherContent_LaterBatchChangesHeld(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	hub := newFakePushHub()
	head := createTasks(t, 1)
	cloneCreates(t, head[0], pushBatchSize-1) // fill the first batch
	require.NoError(t, runUpdate("TEST-1", "retitled", "", "", "", 0, "", ""))
	retitle := eventIDFor(t, "TEST-1", model.OpUpdateField)
	events, err := app.store.ReadPendingEventsByID(ctx, head)
	require.NoError(t, err)
	other := *events[0]
	other.Payload = []byte(`{"title":"another task under this event id"}`)
	_, err = hub.PushEventsResult(ctx, []*model.SyncEvent{&other})
	require.NoError(t, err)

	var stderr bytes.Buffer
	tot, err := pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())
	require.Equal(t, 1, tot.mismatched)
	require.Equal(t, pushBatchSize-1, tot.inserted, "the rest of the first batch is pushed")
	require.False(t, hub.sent(retitle), "the held creation's later change is not sent")
	q, ok := quarantined(t)[retitle]
	require.True(t, ok, "the later change is held")
	require.True(t, strings.HasPrefix(q.Reason, holdDependsPrefix+head[0]), q.Reason)
	require.Equal(t, "pending", syncStatusOf(t, retitle))
}
