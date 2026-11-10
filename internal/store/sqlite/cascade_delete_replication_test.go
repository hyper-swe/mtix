// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-112.17.16.12: a cascade delete soft-deletes every descendant, but it
// used to emit a delete event for the root alone, and the applier never
// cascades (SYNC-DESIGN section 8.3: each descendant carries its own delete
// event). A hub and its clones therefore kept the descendants alive.

// deleteEventCounts returns, per node id, how many delete events the store
// holds.
func deleteEventCounts(t *testing.T, raw *sql.DB) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, ev := range readEventsByOp(t, raw, model.OpDelete) {
		counts[ev.NodeID]++
	}
	return counts
}

// TestDeleteNode_Cascade_EmitsOneDeleteEventPerDescendant: the root and each
// descendant the cascade removes get exactly one delete event; a descendant
// deleted earlier keeps its single earlier event; unrelated nodes, including a
// live sibling whose id shares the root's prefix (MTIX-10 next to MTIX-1) and
// that sibling's child, get none. The events come in the documented order (the
// root, then the descendants in id order, even when they were created out of
// order) and carry the deleting author.
func TestDeleteNode_Cascade_EmitsOneDeleteEventPerDescendant(t *testing.T) {
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-1.2", "MTIX-1")
	mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
	mustCreateNode(t, s, "MTIX-1.1.1", "MTIX-1.1")
	mustCreateNode(t, s, "MTIX-2", "")
	mustCreateNode(t, s, "MTIX-10", "")
	mustCreateNode(t, s, "MTIX-10.1", "MTIX-10")
	require.NoError(t, s.DeleteNode(ctx, "MTIX-1.1.1", false, "alice"))

	require.NoError(t, s.DeleteNode(ctx, "MTIX-1", true, "bob"))

	assert.Equal(t, map[string]int{
		"MTIX-1": 1, "MTIX-1.1": 1, "MTIX-1.2": 1,
		"MTIX-1.1.1": 1, // deleted earlier on its own; the cascade skips it
	}, deleteEventCounts(t, raw), "MTIX-10 and MTIX-10.1 share the prefix text but are not descendants")
	var order, authors []string
	for _, ev := range readEventsByOp(t, raw, model.OpDelete) {
		order = append(order, ev.NodeID)
		authors = append(authors, ev.AuthorID)
	}
	assert.Equal(t, []string{"MTIX-1.1.1", "MTIX-1", "MTIX-1.1", "MTIX-1.2"}, order,
		"the earlier delete, then the root, then the descendants in id order")
	assert.Equal(t, []string{"alice", "bob", "bob", "bob"}, authors, "each event names its deleter")
	for _, id := range []string{"MTIX-10", "MTIX-10.1"} {
		_, err := s.GetNode(ctx, id)
		require.NoError(t, err, "%s stays live on the origin", id)
	}
}

// TestDeleteNode_NoCascade_EmitsOnlyTheTargetsDeleteEvent: a non-cascade delete
// still emits one event and leaves the children alone.
func TestDeleteNode_NoCascade_EmitsOnlyTheTargetsDeleteEvent(t *testing.T) {
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")

	require.NoError(t, s.DeleteNode(ctx, "MTIX-1", false, "alice"))

	assert.Equal(t, map[string]int{"MTIX-1": 1}, deleteEventCounts(t, raw))
}

// TestDeleteNode_Cascade_PulledByReplica_DeletesEveryDescendant is the
// two-store proof: after the origin's pending events reach a replica, the
// replica has the same deleted set, and a later edit of a live sibling still
// applies.
func TestDeleteNode_Cascade_PulledByReplica_DeletesEveryDescendant(t *testing.T) {
	ctx := context.Background()
	origin, rawOrigin := mutationTestStore(t)
	replica, _ := mutationTestStore(t)
	mustCreateNode(t, origin, "MTIX-1", "")
	mustCreateNode(t, origin, "MTIX-1.1", "MTIX-1")
	mustCreateNode(t, origin, "MTIX-1.1.1", "MTIX-1.1")
	mustCreateNode(t, origin, "MTIX-1.2", "MTIX-1")
	mustCreateNode(t, origin, "MTIX-2", "")
	mustCreateNode(t, origin, "MTIX-10", "") // shares the root's prefix text
	mustCreateNode(t, origin, "MTIX-10.1", "MTIX-10")
	pullEvents(t, replica, pushPendingOwnEvents(t, rawOrigin))

	require.NoError(t, origin.DeleteNode(ctx, "MTIX-1", true, "alice"))
	pullEvents(t, replica, pushPendingOwnEvents(t, rawOrigin))

	for _, id := range []string{"MTIX-1", "MTIX-1.1", "MTIX-1.1.1", "MTIX-1.2"} {
		_, err := replica.GetNode(ctx, id)
		require.ErrorIs(t, err, model.ErrNotFound, "%s is deleted on the replica", id)
	}
	for _, id := range []string{"MTIX-2", "MTIX-10", "MTIX-10.1"} {
		_, err := replica.GetNode(ctx, id)
		require.NoError(t, err, "%s is not a descendant and stays live on the replica", id)
	}
}
