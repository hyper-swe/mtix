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
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.17.16.40: a blocks link over a resolved blocker (done, cancelled
// or invalidated) never blocks the dependent, whatever order link_dep and
// the blocker's transition arrive in. Three replicas (Bob, Alice, a fresh
// clone) must end with the same dependent status and the same mtix ready.

// syncReplica switches the CLI to the replica r (a snapshot of app).
func syncReplica(r appContext) { app = r }

// newSyncReplica starts an empty replica and returns its app snapshot.
func newSyncReplica(t *testing.T, author string) appContext {
	t.Helper()
	t.Setenv(sqlite.AuthorIDEnv, author)
	initTestApp(t)
	return app
}

func pullAll(t *testing.T, hub *fakePushHub) {
	t.Helper()
	var stderr bytes.Buffer
	_, _, err := pullLoop(context.Background(), testIngest(&stderr),
		&fakeLateHub{pullEvents: hub.events}, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
}

func pushAll(t *testing.T, hub *fakePushHub) {
	t.Helper()
	var stderr bytes.Buffer
	_, err := pushLoop(context.Background(), &stderr, hub, app.store)
	require.NoError(t, err)
}

// replicaView is what a replica shows: the dependent's status and ready ids.
type replicaView struct {
	dependent model.Status
	ready     string
}

func viewOf(t *testing.T) replicaView {
	t.Helper()
	n, err := app.store.GetNode(context.Background(), "TEST-2")
	require.NoError(t, err)
	return replicaView{dependent: n.Status, ready: captureStdout(t, func() {
		require.NoError(t, runReady("", true))
	})}
}

func TestDepAddBlocks_ResolvedBlockerSync_ReplicasAgree(t *testing.T) {
	for _, resolved := range []model.Status{model.StatusDone, model.StatusCancelled, model.StatusInvalidated} {
		for _, bobFirst := range []bool{true, false} {
			name := string(resolved) + "/alice-pushes-first"
			if bobFirst {
				name = string(resolved) + "/bob-pushes-first"
			}
			t.Run(name, func(t *testing.T) {
				runResolvedBlockerScenario(t, resolved, bobFirst)
			})
		}
	}
}

func runResolvedBlockerScenario(t *testing.T, resolved model.Status, bobFirst bool) {
	hub := newFakePushHub()
	bob := newSyncReplica(t, "agent-bob")
	require.NoError(t, runCreate("blocker", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("dependent", "", "", 3, "", "", "", "", ""))
	pushAll(t, hub)
	alice := newSyncReplica(t, "agent-alice")
	pullAll(t, hub)

	// Bob links and resolves the blocker (the dependent ends open).
	syncReplica(bob)
	require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks"))
	resolveBlocker(t, resolved)
	// Alice links locally before pulling: the blocker is still open for her.
	syncReplica(alice)
	require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks"))

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
	require.Equal(t, model.StatusOpen, want.dependent, "a resolved blocker never blocks the dependent")
	require.Contains(t, want.ready, "TEST-2")
}

// TestRunDepAdd_ResolvedBlocker_DoesNotBlockDependent is the CLI path alone.
func TestRunDepAdd_ResolvedBlocker_DoesNotBlockDependent(t *testing.T) {
	for _, resolved := range []model.Status{model.StatusDone, model.StatusCancelled, model.StatusInvalidated} {
		t.Run(string(resolved), func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("blocker", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("dependent", "", "", 3, "", "", "", "", ""))
			resolveBlocker(t, resolved)
			require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks"))
			n, err := app.store.GetNode(context.Background(), "TEST-2")
			require.NoError(t, err)
			require.Equal(t, model.StatusOpen, n.Status)
		})
	}
}

// resolveBlocker moves TEST-1 to a resolved status (done needs in_progress).
func resolveBlocker(t *testing.T, resolved model.Status) {
	t.Helper()
	if resolved == model.StatusDone {
		require.NoError(t, runTransition("TEST-1", model.StatusInProgress, "start"))
	}
	require.NoError(t, runTransition("TEST-1", resolved, "resolved"))
}
