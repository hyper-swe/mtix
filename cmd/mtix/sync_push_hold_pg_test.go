// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestPushLoop_OversizedEvent_HeldOthersReachRealHub is the PG-gated
// companion of TestPushLoop_OversizedEvent_HeldOthersPushed (MTIX-95.12):
// against a real hub, the event over the wire cap is held locally with
// source push and never reaches the hub, the other events land, and a
// second push sends nothing and fails nothing.
func TestPushLoop_OversizedEvent_HeldOthersReachRealHub(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	prompts := []string{"", overWireCap(), ""}
	ids := make([]string, len(prompts))
	for i, p := range prompts {
		require.NoError(t, runCreate("node", "", "", 3, "", p, "", "", ""))
		ids[i] = eventIDFor(t, fmt.Sprintf("TEST-%d", i+1), model.OpCreateNode)
	}

	var stderr bytes.Buffer
	pushed, _, _, _, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err, stderr.String())
	require.Equal(t, 2, pushed)
	onHub := func(id string) bool {
		var n int
		require.NoError(t, pool.Inner().QueryRow(ctx,
			`SELECT COUNT(*) FROM sync_events WHERE event_id = $1`, id).Scan(&n))
		return n == 1
	}
	require.True(t, onHub(ids[0]))
	require.False(t, onHub(ids[1]), "the oversized event never reaches the hub")
	require.True(t, onHub(ids[2]))
	require.Equal(t, "push", quarantined(t)[ids[1]].Source)

	pushed, _, _, _, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)
	require.Zero(t, pushed, "the held event is not sent again")
}

// TestPushLoop_HeldCreateCollision_RealHub_TeammateTaskIntact is the review
// r1 S0 reproduction against a real hub (MTIX-95.12 round 2): A's creation
// of TEST-1 is held; B creates and pushes its own TEST-1; A claims and
// retitles its TEST-1 and pushes; B pulls. B's task must stay unchanged, and
// no A event for TEST-1 may reach the hub.
func TestPushLoop_HeldCreateCollision_RealHub_TeammateTaskIntact(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	var stderr bytes.Buffer
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	appA := app
	require.NoError(t, runCreate("A's task", "", "", 3, "", overWireCap(), "", "", ""))
	_, _, _, _, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)

	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	appB := app
	require.NoError(t, runCreate("B's own task", "", "", 3, "", "", "", "", ""))
	_, _, _, _, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)

	app = appA
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	require.NoError(t, runClaim("TEST-1", "agent-a"))
	require.NoError(t, runUpdate("TEST-1", "A retitled its task", "", "", "", 0, "", ""))
	_, _, _, _, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)

	app = appB
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	node, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "B's own task", node.Title)
	require.Equal(t, model.StatusOpen, node.Status)
	require.Empty(t, node.Assignee)
	var fromA int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_events WHERE node_id = 'TEST-1' AND author_id = 'agent-a'`).Scan(&fromA))
	require.Zero(t, fromA, "no event of A's held task reaches the hub")
}
