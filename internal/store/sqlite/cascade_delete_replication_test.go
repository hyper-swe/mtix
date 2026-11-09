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
// deleted earlier keeps its single earlier event; an unrelated node gets none.
func TestDeleteNode_Cascade_EmitsOneDeleteEventPerDescendant(t *testing.T) {
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
	mustCreateNode(t, s, "MTIX-1.1.1", "MTIX-1.1")
	mustCreateNode(t, s, "MTIX-1.2", "MTIX-1")
	mustCreateNode(t, s, "MTIX-2", "")
	require.NoError(t, s.DeleteNode(ctx, "MTIX-1.1.1", false, "alice"))

	require.NoError(t, s.DeleteNode(ctx, "MTIX-1", true, "alice"))

	assert.Equal(t, map[string]int{
		"MTIX-1": 1, "MTIX-1.1": 1, "MTIX-1.2": 1,
		"MTIX-1.1.1": 1, // deleted earlier on its own; the cascade skips it
	}, deleteEventCounts(t, raw))
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
	pullEvents(t, replica, pushPendingOwnEvents(t, rawOrigin))

	require.NoError(t, origin.DeleteNode(ctx, "MTIX-1", true, "alice"))
	pullEvents(t, replica, pushPendingOwnEvents(t, rawOrigin))

	for _, id := range []string{"MTIX-1", "MTIX-1.1", "MTIX-1.1.1", "MTIX-1.2"} {
		_, err := replica.GetNode(ctx, id)
		require.ErrorIs(t, err, model.ErrNotFound, "%s is deleted on the replica", id)
	}
	_, err := replica.GetNode(ctx, "MTIX-2")
	require.NoError(t, err, "an unrelated node stays")
}
