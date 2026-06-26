// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// TestPushPull_CarriesUID is the MTIX-30.6 transport round-trip for the
// dual-carry uid (ADR-003 §3, §7 Phase 3): an event pushed with a uid
// must come back from PullEvents with the SAME uid, and a uid-less event
// must come back with an empty uid (omitempty preserved end-to-end).
func TestPushPull_CarriesUID(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	withUID := makeEvent("0193fa00-0000-7000-8000-0000000000a1", "MTIX-1", "alice", 1)
	withUID.UID = withUID.EventID // create self-anchor
	upd := makeEvent("0193fa00-0000-7000-8000-0000000000a2", "MTIX-1", "alice", 2)
	upd.OpType = model.OpUpdateField
	upd.Payload = []byte(`{"field_name":"title","new_value":"\"renamed\""}`)
	upd.UID = withUID.EventID // non-create carries the node's uid
	noUID := makeEvent("0193fa00-0000-7000-8000-0000000000a3", "MTIX-2", "alice", 3)
	// noUID.UID stays empty — emulates an old-CLI event.

	ids, _, err := pool.PushEvents(context.Background(),
		[]*model.SyncEvent{withUID, upd, noUID})
	require.NoError(t, err)
	require.Len(t, ids, 3)

	got, _, err := pool.PullEvents(context.Background(), 0, 100)
	require.NoError(t, err)
	require.Len(t, got, 3)

	byID := map[string]*model.SyncEvent{}
	for _, e := range got {
		byID[e.EventID] = e
	}
	require.Equal(t, withUID.EventID, byID[withUID.EventID].UID,
		"create_node uid must survive the hub round-trip (self-anchor)")
	require.Equal(t, withUID.EventID, byID[upd.EventID].UID,
		"non-create event uid must survive the hub round-trip")
	require.Empty(t, byID[noUID.EventID].UID,
		"a uid-less event must round-trip with empty uid (dual-carry)")
}
