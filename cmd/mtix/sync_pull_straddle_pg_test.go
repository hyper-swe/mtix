// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Real-Postgres tests for a late push that STRADDLES the peer's Lamport
// cursor (MTIX-95.5): a node's create is stamped below the cursor and an
// edit of it above. The cursor pass returns the edit but not the create,
// so the edit's apply finds no node (ErrNotFound). Pull then runs the
// late-event sweep, which delivers the create in Lamport order, and
// retries the cursor pass once. Gated on MTIX_PG_TEST_DSN.

// pullPeerTwice runs two pulls on the peer, each of which must succeed, and
// returns the first one's stderr.
func (f *sweepFixture) pullPeerTwice(t *testing.T) string {
	t.Helper()
	_, errOut := f.pullPeerStreams(t, 100)
	f.pullPeer(t, 100)
	return errOut
}

// TestRunSyncPull_SameClientPushStraddlesCursor_SweepsThenRetries: B,
// offline, creates a node and edits it twenty times, so its Lamport clocks
// run from below the peer's cursor to above it. Both of the peer's next
// pulls succeed and the node arrives with every edit.
func TestRunSyncPull_SameClientPushStraddlesCursor_SweepsThenRetries(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	f.seedSharedNode(t)
	f.editPeer(t, 10)
	f.pushPeer(t)
	f.pullPeer(t, 100)
	cursor := f.peerCursor(t)

	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "B's node")))
	var desc string
	for i := 0; i < 20; i++ {
		desc = fmt.Sprintf("B edit %d", i)
		require.NoError(t, f.b.UpdateNode(ctx, "TEST-2", &store.NodeUpdate{Description: &desc}))
	}
	late := f.pushB(t)
	require.Len(t, late, 21)
	require.Equal(t, model.OpCreateNode, late[0].OpType)
	require.Less(t, late[0].LamportClock, cursor, "precondition: the create is below the cursor")
	require.Greater(t, late[20].LamportClock, cursor, "precondition: the last edit is above the cursor")

	errOut := f.pullPeerTwice(t)

	require.Contains(t, errOut, "retrying the pull once")
	node, err := app.store.GetNode(ctx, "TEST-2")
	require.NoError(t, err)
	require.Equal(t, desc, node.Description, "every edit applied, the last one wins")
	for _, e := range late {
		require.Truef(t, f.appliedOnPeer(t, e.EventID), "event %s (L=%d) applied", e.EventID, e.LamportClock)
	}
	require.Equal(t, late[20].LamportClock, f.peerCursor(t),
		"the retried cursor pass advances the cursor over B's edits")
}

// TestRunSyncPull_CrossClientEditStraddlesCursor_SweepsThenRetries: B,
// offline, creates a node below the peer's cursor; C, who received it,
// edits it with a clock above the peer's cursor. The peer's cursor pass
// returns C's edit without B's create. Both of the peer's next pulls
// succeed and the node arrives with C's edit.
func TestRunSyncPull_CrossClientEditStraddlesCursor_SweepsThenRetries(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	f.seedSharedNode(t)
	f.editPeer(t, 10)
	f.pushPeer(t)
	f.pullPeer(t, 100)
	cursor := f.peerCursor(t)

	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "B's node")))
	fromB := f.pushB(t)
	require.Len(t, fromB, 1)
	require.Less(t, fromB[0].LamportClock, cursor, "precondition: B's create is below the cursor")

	c, err := sqlite.New(filepath.Join(t.TempDir(), ".mtix"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	_, err = c.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = 'agent-c' WHERE key = 'meta.sync.author_id'`)
	require.NoError(t, err)
	var stderr bytes.Buffer
	_, _, err = pullLoop(ctx, &stderr, f.pool, c, 0, 100)
	require.NoError(t, err, "C receives B's node: %s", stderr.String())
	desc := "C's edit of B's node"
	require.NoError(t, c.UpdateNode(ctx, "TEST-2", &store.NodeUpdate{Description: &desc}))
	fromC, err := readPendingBatch(ctx, c, 100)
	require.NoError(t, err)
	require.Len(t, fromC, 1)
	require.Greater(t, fromC[0].LamportClock, cursor, "precondition: C's edit is above the cursor")
	_, _, _, _, err = pushLoop(ctx, &stderr, f.pool, c)
	require.NoError(t, err, "C push: %s", stderr.String())

	errOut := f.pullPeerTwice(t)

	require.Contains(t, errOut, "retrying the pull once")
	node, err := app.store.GetNode(ctx, "TEST-2")
	require.NoError(t, err)
	require.Equal(t, desc, node.Description)
	require.True(t, f.appliedOnPeer(t, fromB[0].EventID))
	require.True(t, f.appliedOnPeer(t, fromC[0].EventID))
}
