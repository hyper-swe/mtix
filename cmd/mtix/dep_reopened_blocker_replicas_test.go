// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// MTIX-112.17.16.40: a blocker reopened after it was resolved blocks its
// dependents again on every replica, whichever of link and reopen a replica
// saw first. Bob and Alice and a fresh clone must show the same dependent
// status and the same mtix ready.

type reopenLinkTiming string

const (
	linkBeforeResolve reopenLinkTiming = "link-before-resolve"
	linkWhileResolved reopenLinkTiming = "link-while-resolved-on-alice"
	linkAfterReopen   reopenLinkTiming = "link-after-reopen"
)

func TestDepAddBlocks_ReopenedBlockerSync_ReplicasAgree(t *testing.T) {
	for _, timing := range []reopenLinkTiming{linkBeforeResolve, linkWhileResolved, linkAfterReopen} {
		for _, bobFirst := range []bool{true, false} {
			name := string(timing) + "/alice-pushes-first"
			if bobFirst {
				name = string(timing) + "/bob-pushes-first"
			}
			t.Run(name, func(t *testing.T) { runReopenedBlockerScenario(t, timing, bobFirst) })
		}
	}
}

func runReopenedBlockerScenario(t *testing.T, timing reopenLinkTiming, bobFirst bool) {
	hub := newFakePushHub()
	bob := newSyncReplica(t, "agent-bob")
	require.NoError(t, runCreate("blocker", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("dependent", "", "", 3, "", "", "", "", ""))
	pushAll(t, hub)
	alice := newSyncReplica(t, "agent-alice")
	pullAll(t, hub)

	syncReplica(bob)
	if timing == linkBeforeResolve {
		require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks"))
	}
	resolveBlocker(t, model.StatusDone)
	pushAll(t, hub)
	syncReplica(alice)
	pullAll(t, hub)

	syncReplica(bob)
	require.NoError(t, runTransition("TEST-1", model.StatusOpen, "reopen"))
	if timing == linkAfterReopen {
		pushAll(t, hub)
		syncReplica(alice)
		pullAll(t, hub)
	}
	syncReplica(alice)
	if timing != linkBeforeResolve {
		require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks"))
	}

	order := []appContext{alice, bob}
	if bobFirst {
		order = []appContext{bob, alice}
	}
	for _, r := range order {
		syncReplica(r)
		pushAll(t, hub)
	}
	for _, r := range []appContext{bob, alice, bob, alice} {
		syncReplica(r)
		pullAll(t, hub)
		pushAll(t, hub)
	}
	syncReplica(bob)
	want := viewOf(t)
	syncReplica(alice)
	require.Equal(t, want, viewOf(t), "alice and bob agree")

	fresh := newSyncReplica(t, "agent-fresh")
	var stderr bytes.Buffer
	_, _, err := cloneLoop(context.Background(), &stderr,
		&fakeLateHub{pullEvents: hub.events}, fresh.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	require.Equal(t, want, viewOf(t), "a fresh clone agrees")
	require.Equal(t, model.StatusBlocked, want.dependent, "a reopened blocker blocks its dependent")
	require.NotContains(t, want.ready, "TEST-2")
}

// TestRunDepAdd_ResolvedBlockerWithRelatedLink_DoesNotBlockDependent: a
// related or discovered_from link from an open node never blocks, so a blocks
// link over a resolved blocker still leaves the dependent open.
func TestRunDepAdd_ResolvedBlockerWithRelatedLink_DoesNotBlockDependent(t *testing.T) {
	for _, linkType := range []string{"related", "discovered_from"} {
		t.Run(linkType, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("blocker", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("dependent", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runDepAdd("TEST-3", "TEST-2", linkType))
			resolveBlocker(t, model.StatusDone)
			require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks"))
			n, err := app.store.GetNode(context.Background(), "TEST-2")
			require.NoError(t, err)
			require.Equal(t, model.StatusOpen, n.Status)
		})
	}
}
