// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// Every event push sends carries its uid (MTIX-91), on both the pending path
// and the path of a held event released later (MTIX-95.12). The hub resolves
// a registered create's effective uid as its stored uid, falling back to its
// event id when that is NULL, so an event sent without its uid makes the
// hub's same-logical-node no-op unreachable for that node.

// nodeUIDOf returns the uid of the local node nodeID.
func nodeUIDOf(t *testing.T, nodeID string) string {
	t.Helper()
	var uid string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT COALESCE(uid, '') FROM nodes WHERE id = ?`, nodeID).Scan(&uid))
	require.NotEmpty(t, uid, "%s must have a uid to carry", nodeID)
	return uid
}

// sentEvent returns the event eventID as the hub accepted it, or nil.
func sentEvent(hub *fakePushHub, eventID string) *model.SyncEvent {
	for _, e := range hub.events {
		if e.EventID == eventID {
			return e
		}
	}
	return nil
}

// TestPushLoop_PendingAndReleasedHeldEvents_CarryUID: a create pushed
// straight from the pending queue, and a create held for its clock and
// released by a later push, both reach the hub with their node's uid.
func TestPushLoop_PendingAndReleasedHeldEvents_CarryUID(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("pending", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("held, then released", "", "", 3, "", "", "", "", ""))
	plain := eventIDFor(t, "TEST-1", model.OpCreateNode)
	clocked := eventIDFor(t, "TEST-2", model.OpCreateNode)
	stampInFuture(t, clocked)
	hub := newFakePushHub()

	require.NoError(t, pushWith(t, hub))
	sent := sentEvent(hub, plain)
	require.NotNil(t, sent, "the pending create is pushed")
	require.Equal(t, nodeUIDOf(t, "TEST-1"), sent.UID,
		"MTIX-91: an event pushed from the pending queue carries its node's uid")
	require.False(t, hub.sent(clocked), "the create stamped ahead is held")
	require.Equal(t, 1, pushHoldCount(t))

	stampAt(t, clocked, time.Now())
	require.NoError(t, pushWith(t, hub))
	released := sentEvent(hub, clocked)
	require.NotNil(t, released, "the released create is pushed")
	require.Equal(t, nodeUIDOf(t, "TEST-2"), released.UID,
		"MTIX-91: a held event released by a later push carries its node's uid")
	require.Zero(t, pushHoldCount(t), "the hold is released")
}
