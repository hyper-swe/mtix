// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// Tests of the creation gate of push (MTIX-95.37), against the fake hub.
// INVARIANT: push sends no event about a task, below it, or linking to it
// while its creation is pending; after the hub renumbers a creation, every
// pending event of the task is sent under the new numbers.

// sentOrder returns, in the order the hub received them, "op@number" of each
// event the fake hub accepted.
func sentOrder(h *fakePushHub) []string {
	out := make([]string, 0, len(h.events))
	for _, e := range h.events {
		out = append(out, fmt.Sprintf("%s@%s", e.OpType, e.NodeID))
	}
	return out
}

// opOf returns the op_type of the local event id.
func opOf(t *testing.T, eventID string) string {
	t.Helper()
	var op string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT op_type FROM sync_events WHERE event_id = ?`, eventID).Scan(&op))
	return op
}

// TestPushLoop_RenumberedCreation_DependentsGoOutUnderTheNewNumbers: the hub
// asks for a renumber of TEST-1; the claim, the edit, the child's creation
// and edit and the links into the task, all queued before the renumber, are
// sent only after the renumbered creation, and under the new numbers.
func TestPushLoop_RenumberedCreation_DependentsGoOutUnderTheNewNumbers(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("A task", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("A other", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runClaim("TEST-1", "agent-a"))
	require.NoError(t, runUpdate("TEST-1", "A retitled", "", "", "", 0, "", ""))
	require.NoError(t, runCreate("A child", "TEST-1", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-1.1", "A child retitled", "", "", "", 0, "", ""))
	require.NoError(t, runDepAdd("TEST-2", "TEST-1", "blocks"))
	require.NoError(t, runDepAdd("TEST-1", "TEST-2", "related"))
	create := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	hub.renumberOnce = map[string]bool{create: true}

	var stderr bytes.Buffer
	_, err := pushLoop(context.Background(), &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())

	// The creations; the renumbered creation; the events that waited for the
	// creations; the edit of the child, which waited for the child's creation.
	require.Len(t, hub.calls, 4)
	for _, id := range hub.calls[0] {
		require.Equal(t, string(model.OpCreateNode), opOf(t, id), "the first call carries creations only")
	}
	require.Len(t, hub.calls[1], 1, "the renumbered creation goes out alone")
	for _, e := range hub.events {
		require.NotRegexp(t, `^TEST-1(\.|$)`, e.NodeID, "no accepted event names the old number: %s", e.OpType)
		if strings.Contains(string(e.Payload), `"depends_on_node_id"`) {
			require.NotRegexp(t, `"depends_on_node_id":"TEST-1[".]`, string(e.Payload))
		}
	}
	require.Contains(t, sentOrder(hub), "claim@TEST-3")
	require.Contains(t, sentOrder(hub), "create_node@TEST-3.1")
	require.Contains(t, sentOrder(hub), "update_field@TEST-3.1")
}

// TestPushLoop_DependentsOfManyCreations_AllSentAfterTheirCreation: across
// batch boundaries every event is sent exactly once, after the creation of
// its task, and the loop ends with nothing pending (the deferred events are
// read again, not skipped).
func TestPushLoop_DependentsOfManyCreations_AllSentAfterTheirCreation(t *testing.T) {
	initTestApp(t)
	const tasks = pushBatchSize + 50
	for i := 0; i < tasks; i++ {
		require.NoError(t, runCreate(fmt.Sprintf("task %d", i), "", "", 3, "", "", "", "", ""))
		require.NoError(t, runClaim(fmt.Sprintf("TEST-%d", i+1), "agent-a"))
	}
	hub := newFakePushHub()

	var stderr bytes.Buffer
	tot, err := pushLoop(context.Background(), &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())

	require.Equal(t, 2*tasks, tot.pushed(), "every event is pushed")
	created := map[string]bool{}
	for _, e := range hub.events {
		if e.OpType == model.OpCreateNode {
			created[e.NodeID] = true
			continue
		}
		require.True(t, created[e.NodeID], "%s of %s is sent after its creation", e.OpType, e.NodeID)
	}
	var pending int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`).Scan(&pending))
	require.Zero(t, pending)
}

// TestPushLoop_CreationTheHubNeverAccepts_DependentsAreNeverSent: a creation
// that is not acknowledged (held by push here) keeps what depends on it from
// the hub, and the push ends.
func TestPushLoop_CreationTheHubNeverAccepts_DependentsAreNeverSent(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("A task", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runClaim("TEST-1", "agent-a"))
	create := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	hub.refuse = map[string]bool{create: true}

	var stderr bytes.Buffer
	tot, err := pushLoop(context.Background(), &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())

	require.Zero(t, tot.pushed())
	require.Empty(t, hub.events)
	require.Len(t, hub.calls, 1, "the claim never goes out while its creation is unacknowledged")
}
