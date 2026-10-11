// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Native holder's final empty read precedes a different process's committed
// event and clean contended push. No later push command may rescue that event.
func TestRunSyncPush_ProcessEmptyQueueHandoff(t *testing.T) {
	pool := openCmdHub(t) // Only parent creates and migrates the owned database.
	dir := t.TempDir()
	store := newDaemonTestStore(t, dir)
	holder := startPushHandoffChild(t, dir, "holder")
	holder.frame(t, "empty")
	contender := startPushHandoffChild(t, dir, "contender")
	contender.frame(t, "ready")
	contender.release(t)
	committed := contender.frame(t, "skipped")
	require.NotEmpty(t, committed.EventID)
	contender.join(t) // The native commit and contention finish before holder resumes.
	holder.release(t)
	holder.frame(t, "done")
	holder.join(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var local string
	// Check the actual committed journal event; this assertion does not push.
	require.NoError(t, store.WriteDB().QueryRowContext(ctx,
		`SELECT sync_status FROM sync_events WHERE event_id = ?`, committed.EventID).Scan(&local))
	var delivered int
	// Query only this event in the real hub; no controller or fake transport.
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_events WHERE event_id = $1`, committed.EventID).Scan(&delivered))
	t.Logf("native handoff event=%s local=%s hub_count=%d", committed.EventID, local, delivered)
	require.Equal(t, "pushed", local, "original holder must autonomously push the committed handoff event")
	require.Equal(t, 1, delivered, "actual PG hub must receive the handoff event without another command")
}
func TestPushHandoff_ProcessProtocol(t *testing.T) {
	child := startPushHandoffChild(t, t.TempDir(), "protocol")
	child.frame(t, "ready")
	child.release(t)
	child.frame(t, "done")
	child.join(t)
}
