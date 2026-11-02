// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// inProcessChild stands in for the detached child: it runs the child's body
// to completion in this process, so the test can observe the hub afterwards.
func inProcessChild(t *testing.T) func(string, string, []string) error {
	return func(_, _ string, argv []string) error {
		require.Equal(t, childPushArgv(), argv)
		return runAddressedSendPush(context.Background(), io.Discard,
			transport.Options{InsecureTLS: true}, utcNow)
	}
}

// An addressed send on an enrolled activity-mode board reaches the hub with
// no manual push, its node's create first (ADR-007 D21, MTIX-112.20.2).
func TestAddressedSend_EnrolledActivityBoard_PushesPendingPrefix_PG(t *testing.T) {
	pool := openCmdHub(t) // sets the hub DSN: this checkout resolves its own hub
	initTestApp(t)
	app.detachedSpawn = inProcessChild(t)
	id := seedOneNode(t)
	other := seedOneNode(t)

	out := captureStdout(t, func() { require.NoError(t, runComment(id, "please review", "agent-b")) })
	assert.Contains(t, out, "queued for hub push")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rows, err := pool.Inner().Query(ctx,
		`SELECT node_id, op_type FROM sync_events ORDER BY lamport_clock, event_id`)
	require.NoError(t, err)
	defer rows.Close()
	order := map[string][]string{}
	for rows.Next() {
		var node, op string
		require.NoError(t, rows.Scan(&node, &op))
		order[node] = append(order[node], op)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"create_node", "comment"}, order[id], "create reaches the hub before its comment")
	assert.Equal(t, []string{"create_node"}, order[other], "the whole pending prefix goes, not only the comment")

	var pending int
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`).Scan(&pending))
	assert.Zero(t, pending, "nothing stays pending after the send")
}

// V-X3 round trip: a send on one checkout is on the hub without a manual
// push, and a second checkout pulls it into the addressee's inbox.
func TestAddressedSend_RoundTrip_SecondCheckoutPullsIt_PG(t *testing.T) {
	openCmdHub(t)
	initTestApp(t)
	app.detachedSpawn = inProcessChild(t)
	id := seedOneNode(t)
	require.NoError(t, runComment(id, "round trip ping", "agent-b"))

	initTestApp(t) // the second checkout: a fresh board against the same hub
	var out, diagnostic bytes.Buffer
	require.NoError(t, runSyncPull(context.Background(), &out, &diagnostic, nil,
		transport.Options{InsecureTLS: true}, 100), diagnostic.String())
	got, err := app.store.InboxList(context.Background(), "agent-b")
	require.NoError(t, err)
	require.Len(t, got, 1, "the addressed comment arrived in the addressee's inbox")
	assert.Contains(t, got[0].Body, "round trip ping")
}
