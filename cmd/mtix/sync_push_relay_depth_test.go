// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// relayHeldTree publishes MTIX-1, then makes MTIX-1.1 and MTIX-1.1.1 each
// with a title edit, two unrelated tasks and a link between them, and pushes
// with a hub that renumbers MTIX-1. It returns the store.
func relayHeldTree(t *testing.T) *sqlite.Store {
	t.Helper()
	ctx := t.Context()
	s := movesTestStore(t)
	root := ownCreate(t, s)
	publish(t, s, root)
	for _, n := range [][3]string{{"MTIX-1.1", "MTIX-1", "child"}, {"MTIX-1.1.1", "MTIX-1.1", "grandchild"},
		{"MTIX-2", "", "other a"}, {"MTIX-3", "", "other b"}} {
		require.NoError(t, s.CreateNode(ctx, movesNode(n[0], n[1], n[2])))
	}
	for _, id := range []string{"MTIX-1.1", "MTIX-1.1.1"} {
		title := "edited " + id
		require.NoError(t, s.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title}))
	}
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: "MTIX-2", ToID: "MTIX-3", DepType: model.DepTypeBlocks}))
	hub := newFakePushHub()
	hub.renumberOnce = map[string]bool{root: true}
	_, err := pushLoop(ctx, io.Discard, hub, s)
	require.NoError(t, err)
	return s
}

// Every event under a relay-held creation, at any depth, and every link made
// after it, is told the hold is permanent, never that it resolves or pushes.
func TestCheckHeldPushEvents_RelaySubtreeAndLinksArePermanent(t *testing.T) {
	s := relayHeldTree(t)
	all, err := s.HeldPushEvents(t.Context(), 100)
	require.NoError(t, err)
	relay := relayHeldIDs(all)
	seen := map[string]bool{}
	for _, h := range all {
		got := heldPushGuidance(h, relay)
		seen[h.NodeID+" "+h.OpType] = true
		require.Contains(t, got, "permanent", "%s %s", h.NodeID, h.OpType)
		require.NotContains(t, got, "resolves with", "%s %s", h.NodeID, h.OpType)
		require.NotContains(t, got, "it pushes once", "%s %s", h.NodeID, h.OpType)
	}
	for _, want := range []string{"MTIX-1 create_node", "MTIX-1.1 create_node", "MTIX-1.1 update_field",
		"MTIX-1.1.1 create_node", "MTIX-1.1.1 update_field", "MTIX-2 link_dep"} {
		require.True(t, seen[want], "%s must be held: %v", want, seen)
	}
}

// The same wording reaches doctor's detail, which is what --json carries.
func TestCheckHeldPushEvents_RelaySubtreeDetail(t *testing.T) {
	s := relayHeldTree(t)
	ok, detail := checkHeldPushEvents(t.Context(), s)
	require.False(t, ok)
	require.NotContains(t, detail, "resolves with")
	require.NotContains(t, detail, "it pushes once")
}

// A dependent past the fifth listed hold is still recognised (the map is read
// from every held event, not the listed ones), and a held creation whose
// reason is not relay-carried is not in the map.
func TestRelayHeldIDs_ReadsAllHoldsAndTheReason(t *testing.T) {
	var held []sqlite.HeldPushEvent
	for i := 0; i < heldPushListed; i++ {
		held = append(held, sqlite.HeldPushEvent{EventID: "PLAIN-" + string(rune('a'+i)), Reason: holdRefusedPrefix + "too big"})
	}
	held = append(held,
		sqlite.HeldPushEvent{EventID: "R", Reason: holdRefusedPrefix + "x " + holdRelayCarried},
		sqlite.HeldPushEvent{EventID: "D1", Reason: holdDependsPrefix + "R (N)"},
		sqlite.HeldPushEvent{EventID: "D2", Reason: holdDependsPrefix + "D1 (N)"},
		sqlite.HeldPushEvent{EventID: "L", Reason: holdDependsPrefix + "R (N): " + holdLinkWait})
	got := relayHeldIDs(held)
	require.True(t, got["R"] && got["D1"] && got["D2"] && got["L"])
	require.False(t, got["PLAIN-a"])
	other := relayHeldIDs(held[:heldPushListed])
	require.Empty(t, other)
	require.Contains(t, heldPushGuidance(held[len(held)-1], got), "permanent")
	require.True(t, strings.Contains(heldPushGuidance(held[len(held)-2], got), "permanent"))
}

// A link behind a held creation that is not relay-carried still pushes once
// the creation clears.
func TestHeldPushGuidance_PlainLinkWaitUnchanged(t *testing.T) {
	l := sqlite.HeldPushEvent{EventID: "L", OpType: "link_dep", Reason: holdDependsPrefix + "C (N): " + holdLinkWait}
	require.Contains(t, heldPushGuidance(l, nil), "it pushes once")
}
