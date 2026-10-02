// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// Tests for MTIX-95.31.16 on the automatic import after a git pull: the
// replace it runs can give a task the file's uid (a same-title task whose
// uid was assigned at upgrade), so it carries the uid onto the task's
// pending sync events like the merge does, under the push lock; while a
// push runs it skips and the next command runs it.

// queuePending inserts a pending sync_events row for node PROJ-1 carrying uid.
func queuePending(t *testing.T, f *guardFixture, id, uid string) {
	t.Helper()
	_, err := f.store.WriteDB().ExecContext(context.Background(), `INSERT INTO sync_events
		(event_id, project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock, vector_clock,
		 author_id, author_machine_hash, sync_status, created_at, uid)
		VALUES (?, 'PROJ', 'PROJ-1', 'update_field', '{}', 1, 1, '{}', 'a', 'm', 'pending', '2026-01-01T00:00:00Z', ?)`, id, uid)
	require.NoError(t, err)
}

// pendingUID returns the uid of the sync event id.
func pendingUID(t *testing.T, f *guardFixture, id string) string {
	t.Helper()
	var uid string
	require.NoError(t, f.store.WriteDB().QueryRowContext(context.Background(),
		`SELECT COALESCE(uid, '') FROM sync_events WHERE event_id = ?`, id).Scan(&uid))
	return uid
}

// upgradedClones returns two clones of one board whose shared tasks carry
// different backfilled uids, with the first clone's board pulled into the
// second's .mtix/tasks.json but not yet imported.
func upgradedClones(t *testing.T) (origin, b *guardFixture) {
	t.Helper()
	origin = newGuardFixture(t)
	b = clones(t, origin, 1)[0]
	for _, x := range []*guardFixture{origin, b} {
		backfillUIDs(t, x)
	}
	require.NoError(t, origin.svc.AutoExport(context.Background(), origin.mtixDir))
	shareBoard(t, origin, b)
	return origin, b
}

// TestAutoImport_AdoptingUID_CarriesUIDOntoPendingEvents verifies the
// automatic import that adopts the file's uid for a task moves the uid of
// the task's pending events with it, and leaves another task's events and
// pushed events alone.
func TestAutoImport_AdoptingUID_CarriesUIDOntoPendingEvents(t *testing.T) {
	ctx := context.Background()
	origin, b := upgradedClones(t)
	mine, other := uidAt(t, b, "PROJ-1"), uidAt(t, b, "PROJ-2")
	queuePending(t, b, "pending-1", mine)
	_, err := b.store.WriteDB().ExecContext(ctx,
		`INSERT INTO sync_events SELECT 'pushed-1', project_prefix, node_id, op_type, payload, wall_clock_ts,
		 lamport_clock, vector_clock, author_id, author_machine_hash, 'pushed', created_at, retained_until, uid
		 FROM sync_events WHERE event_id = 'pending-1'`)
	require.NoError(t, err)
	_, err = b.store.WriteDB().ExecContext(ctx, `UPDATE sync_events SET node_id = 'PROJ-2', uid = ?
		WHERE event_id = 'pushed-1'`, other)
	require.NoError(t, err)

	require.NoError(t, b.svc.AutoImport(ctx, b.mtixDir))

	theirs := uidAt(t, origin, "PROJ-1")
	require.Equal(t, theirs, uidAt(t, b, "PROJ-1"), "the file's uid is adopted")
	assert.Equal(t, theirs, pendingUID(t, b, "pending-1"), "the pending event follows the task")
	assert.Equal(t, other, pendingUID(t, b, "pushed-1"), "another task's pushed event is untouched")
}

// TestAutoImport_PushRunning_SkipsWithNoticeAndRunsNextTime verifies the
// automatic import never fails the command and never loses the pulled board
// while a push holds the push lock: it imports nothing, prints one line, and
// runs on the next command once the push has ended.
func TestAutoImport_PushRunning_SkipsWithNoticeAndRunsNextTime(t *testing.T) {
	ctx := context.Background()
	origin, b := upgradedClones(t)
	mine := uidAt(t, b, "PROJ-1")
	queuePending(t, b, "pending-1", mine)

	b.notices.Reset()
	lock, err := pushlock.Acquire(b.mtixDir)
	require.NoError(t, err)
	require.NoError(t, b.svc.AutoImport(ctx, b.mtixDir), "the command is not failed")
	assert.Contains(t, b.notices.String(), "a push is running")
	assert.Equal(t, 1, countLines(b.notices.String()), "one line")
	assert.Equal(t, mine, uidAt(t, b, "PROJ-1"), "nothing is imported")
	assert.Equal(t, mine, pendingUID(t, b, "pending-1"))
	require.NoError(t, lock.Release())

	require.NoError(t, b.svc.AutoImport(ctx, b.mtixDir), "the next command imports the pending board")
	theirs := uidAt(t, origin, "PROJ-1")
	assert.Equal(t, theirs, uidAt(t, b, "PROJ-1"))
	assert.Equal(t, theirs, pendingUID(t, b, "pending-1"))
	free, err := pushlock.Acquire(b.mtixDir)
	require.NoError(t, err, "the lock is free after the import")
	require.NoError(t, free.Release())
	_, statErr := os.Stat(filepath.Join(b.mtixDir, "tasks.json"))
	require.NoError(t, statErr)
}

// countLines counts the non-empty lines of s.
func countLines(s string) int {
	n := 0
	for _, l := range splitLines(s) {
		if l != "" {
			n++
		}
	}
	return n
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}
