// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestBlockers_RemoveSharedKey_ForgetsOnlyThatCreation: two held creations
// filed under the same number (a task and a re-emitted creation of it, or a
// creation whose event names another's current number) are removed one at
// a time: each removal forgets that creation only, under every key it was
// filed under, and once both are gone nothing blocks (MTIX-95.12).
func TestBlockers_RemoveSharedKey_ForgetsOnlyThatCreation(t *testing.T) {
	bs := newBlockers()
	first := &blocker{eventID: "e1", nodeID: "P-1", lamport: 1}
	second := &blocker{eventID: "e2", nodeID: "P-2", lamport: 2}
	bs.add(first, "P-5")
	bs.add(second, "P-5")
	byUID := holdCheck{eventID: "x", nodeID: "P-5.1", op: model.OpUpdateField, lamport: 9,
		subject: sqlite.PushSubject{UID: "u", CurrentNodeID: "P-5.1"}}
	byNumber := holdCheck{eventID: "y", nodeID: "P-5.1", op: model.OpUpdateField, lamport: 9}

	bs.remove("e2")
	for _, c := range []holdCheck{byUID, byNumber} {
		b, _, ok := bs.holdFor(c)
		require.True(t, ok)
		require.Same(t, first, b)
	}
	bs.remove("e1")
	for _, c := range []holdCheck{byUID, byNumber} {
		_, _, ok := bs.holdFor(c)
		require.False(t, ok, "a removed creation blocks nothing")
	}
	require.True(t, bs.empty())
}

// TestBlockers_Link_WaitsForEarliestHeldCreationBeforeIt: a link or unlink
// is held for the earliest held creation when that creation comes before it
// in queue order, whatever number it names, with a reason that says why;
// when the earliest one is released, the next earliest takes over; a link
// made before every held creation is not held (MTIX-95.12).
func TestBlockers_Link_WaitsForEarliestHeldCreationBeforeIt(t *testing.T) {
	bs := newBlockers()
	early := &blocker{eventID: "e1", nodeID: "P-1", lamport: 5}
	late := &blocker{eventID: "e2", nodeID: "P-2", lamport: 7}
	last := &blocker{eventID: "e3", nodeID: "P-3", lamport: 9}
	bs.add(late, "P-2")
	bs.add(last, "P-3")
	bs.add(early, "P-1")
	link := holdCheck{eventID: "l", nodeID: "P-9", op: model.OpLinkDep, lamport: 8}
	unlink := holdCheck{eventID: "u", nodeID: "P-9", op: model.OpUnlinkDep, lamport: 6}

	b, why, ok := bs.holdFor(link)
	require.True(t, ok)
	require.Same(t, early, b)
	require.True(t, strings.HasPrefix(why, holdDependsPrefix+"e1 (P-1): "), why)
	require.Contains(t, why, holdLinkWait)
	_, _, ok = bs.holdFor(unlink)
	require.True(t, ok, "the unlink comes after the earliest held creation")

	bs.remove("e1")
	b, _, ok = bs.holdFor(link)
	require.True(t, ok)
	require.Same(t, late, b, "the next earliest held creation takes over, not a later one")
	_, _, ok = bs.holdFor(unlink)
	require.False(t, ok, "no held creation comes before the unlink any more")
	_, _, ok = bs.holdFor(holdCheck{eventID: "a", op: model.OpLinkDep, lamport: 7})
	require.False(t, ok, "same clock, earlier event id: before the creation")
}
