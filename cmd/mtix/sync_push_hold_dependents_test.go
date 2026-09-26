// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Held creations and their dependents, and temporary holds (MTIX-95.12
// round 2, review r1 S0 and S2).
//
// In 0.5.x an event names its node by display id (node_id) and carries no
// uid a teammate's replica can check. When push holds a task's creation but
// sends the task's later events, a teammate who created their own task with
// the same number applies those events to it. So every later event that
// depends on a held creation is held too: any event of that task, the
// creation of a child (and so its whole subtree) and a dependency that names
// the task. A refusal that only depends on the clock (a stamp more than 24h
// ahead) is a temporary hold: every push checks it again and releases it,
// with its dependents, once it passes.

// TestPushLoop_HeldCreateCollision_TeammateTaskIntact is the review r1 S0
// reproduction: A's creation of TEST-1 is held (its prompt is over the wire
// cap), then A claims and retitles TEST-1 and pushes. B created its own
// TEST-1. After B pulls, B's task must be unchanged: none of A's TEST-1
// events reached the hub.
func TestPushLoop_HeldCreateCollision_TeammateTaskIntact(t *testing.T) {
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("A's task", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runClaim("TEST-1", "agent-a"))
	require.NoError(t, runUpdate("TEST-1", "A retitled its task", "", "", "", 0, "", ""))
	hub := newFakePushHub()
	var stderr bytes.Buffer
	_, _, _, _, err := pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err)

	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	require.NoError(t, runCreate("B's own task", "", "", 3, "", "", "", "", ""))
	_, _, err = pullLoop(ctx, testIngest(&stderr), &fakeLateHub{pullEvents: hub.events}, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	node, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "B's own task", node.Title)
	require.Equal(t, model.StatusOpen, node.Status)
	require.Empty(t, node.Assignee)
	for _, e := range hub.events {
		require.NotEqual(t, "TEST-1", e.NodeID, "no event of A's held task reaches the hub: %s", e.OpType)
	}
}

// TestPushLoop_HeldCreate_DependentsHeld: every later event that depends on
// a held creation is held with a reason naming the nearest held creation
// above it (the held task's own, or a held child's), whether it is in the
// same push or a later one; events of other tasks still push.
func TestPushLoop_HeldCreate_DependentsHeld(t *testing.T) {
	tests := []struct {
		name      string
		pushFirst bool // push once between the held creation and the mutation
		mutate    func(t *testing.T)
		node      string
		op        model.OpType
		held      bool
		nearest   string // the task whose creation the reason names; "" for TEST-1
	}{
		{"edit of the held task", false, func(t *testing.T) {
			require.NoError(t, runUpdate("TEST-1", "retitled", "", "", "", 0, "", ""))
		}, "TEST-1", model.OpUpdateField, true, ""},
		{"claim of the held task", false, func(t *testing.T) {
			require.NoError(t, runClaim("TEST-1", "agent-a"))
		}, "TEST-1", model.OpClaim, true, ""},
		{"edit made after an earlier push", true, func(t *testing.T) {
			require.NoError(t, runUpdate("TEST-1", "retitled later", "", "", "", 0, "", ""))
		}, "TEST-1", model.OpUpdateField, true, ""},
		{"child created under the held task", false, func(t *testing.T) {
			require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
		}, "TEST-1.1", model.OpCreateNode, true, ""},
		{"edit of a grandchild, the child held by an earlier push", true, func(t *testing.T) {
			require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
			var stderr bytes.Buffer
			_, _, _, _, err := pushLoop(context.Background(), &stderr, newFakePushHub(), app.store)
			require.NoError(t, err)
			require.NoError(t, runCreate("grandchild", "TEST-1.1", "", 3, "", "", "", "", ""))
			require.NoError(t, runUpdate("TEST-1.1.1", "grandchild retitled", "", "", "", 0, "", ""))
		}, "TEST-1.1.1", model.OpUpdateField, true, "TEST-1.1.1"},
		{"dependency that names the held task", false, func(t *testing.T) {
			require.NoError(t, runDepAdd("TEST-2", "TEST-1", "related"))
		}, "TEST-2", model.OpLinkDep, true, ""},
		{"dependency on the held task removed", false, func(t *testing.T) {
			require.NoError(t, runDepAdd("TEST-2", "TEST-1", "related"))
			require.NoError(t, runDepRemove("TEST-2", "TEST-1", "related"))
		}, "TEST-2", model.OpUnlinkDep, true, ""},
		{"dependency of the held task on another", false, func(t *testing.T) {
			require.NoError(t, runDepAdd("TEST-1", "TEST-2", "related"))
		}, "TEST-1", model.OpLinkDep, true, ""},
		{"edit of an unrelated task", false, func(t *testing.T) {
			require.NoError(t, runUpdate("TEST-2", "other retitled", "", "", "", 0, "", ""))
		}, "TEST-2", model.OpUpdateField, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
			root := eventIDFor(t, "TEST-1", model.OpCreateNode)
			require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
			hub := newFakePushHub()
			var stderr bytes.Buffer
			if tt.pushFirst {
				_, _, _, _, err := pushLoop(ctx, &stderr, hub, app.store)
				require.NoError(t, err)
			}
			tt.mutate(t)
			_, _, _, _, err := pushLoop(ctx, &stderr, hub, app.store)
			require.NoError(t, err)

			if !tt.held {
				require.True(t, hub.accepted[eventIDFor(t, tt.node, tt.op)], "an unrelated event pushes")
				return
			}
			if tt.nearest != "" {
				root = eventIDFor(t, tt.nearest, model.OpCreateNode)
			}
			requireHeldDependent(t, hub, tt.node, tt.op, root)
		})
	}
}

// TestPushLoop_FutureStampedCreate_TemporaryHoldReleased: a creation stamped
// more than 24h ahead is held as a temporary clock hold, and its later claim
// as its dependent. Every push checks the hold again: once the stamp is
// within the limit, the creation and its claim are released together and
// pushed as ordinary events; while the stamp is still ahead, both stay held
// and the attempt is counted.
func TestPushLoop_FutureStampedCreate_TemporaryHoldReleased(t *testing.T) {
	tests := []struct {
		name  string
		fixed bool
	}{
		{"stamp corrected before the next push", true},
		{"stamp still too far ahead at the next push", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			require.NoError(t, runCreate("now", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("ahead", "", "", 3, "", "", "", "", ""))
			create := eventIDFor(t, "TEST-2", model.OpCreateNode)
			stampInFuture(t, create)
			require.NoError(t, runClaim("TEST-2", "agent-a"))
			claim := eventIDFor(t, "TEST-2", model.OpClaim)

			hub := newFakePushHub()
			var stderr bytes.Buffer
			pushed, _, _, _, err := pushLoop(ctx, &stderr, hub, app.store)
			require.NoError(t, err)
			require.Equal(t, 1, pushed, "only TEST-1 pushes")
			held := quarantined(t)
			require.True(t, strings.HasPrefix(held[create].Reason, "temporary: clock"), held[create].Reason)
			require.True(t, strings.HasPrefix(held[claim].Reason, "depends on held create of "+create), held[claim].Reason)

			if tt.fixed {
				stampAt(t, create, time.Now())
			}
			next := newFakePushHub()
			stderr.Reset()
			pushed, _, _, _, err = pushLoop(ctx, &stderr, next, app.store)
			require.NoError(t, err)
			if !tt.fixed {
				require.Zero(t, pushed)
				held = quarantined(t)
				require.Equal(t, 2, held[create].Attempts, "the temporary hold is checked on every push")
				require.Contains(t, held, claim)
				return
			}
			require.Equal(t, 2, pushed, "the released creation and its claim push")
			require.Equal(t, [][]string{{create, claim}}, next.calls, "creation first, then its claim, in one push")
			require.Empty(t, quarantined(t), "both holds are released")
			require.Contains(t, stderr.String(), "released")
		})
	}
}

// TestPushLoop_InvalidDependent_KeepsDependsReason: an event that depends on
// a held creation is held as its dependent even when it is itself invalid,
// so its fix is the creation's, not its own (review r2 mutant M11: the
// dependency is checked before any other rule).
func TestPushLoop_InvalidDependent_KeepsDependsReason(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T)
		op     model.OpType
	}{
		{"prompt edit over the wire cap", func(t *testing.T) {
			require.NoError(t, runPrompt("TEST-1", overWireCap()))
		}, model.OpSetPrompt},
		{"claim stamped more than 24h ahead", func(t *testing.T) {
			require.NoError(t, runClaim("TEST-1", "agent-a"))
			stampInFuture(t, eventIDFor(t, "TEST-1", model.OpClaim))
		}, model.OpClaim},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
			root := eventIDFor(t, "TEST-1", model.OpCreateNode)
			tt.mutate(t)
			hub := newFakePushHub()
			var stderr bytes.Buffer
			_, _, _, _, err := pushLoop(context.Background(), &stderr, hub, app.store)
			require.NoError(t, err)
			requireHeldDependent(t, hub, "TEST-1", tt.op, root)
		})
	}
}
