// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestWithPushLock_ReadsAndHoldsInOneTransaction (MTIX-95.39): inside the
// lock push reads the held creations and the batch's tasks, records holds
// and counts them, all in one write transaction; a held row is visible to
// the lock's own reads before the commit and to the store after it.
func TestWithPushLock_ReadsAndHoldsInOneTransaction(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	err := s.WithPushLock(ctx, func(l sqlite.PushLocked) error {
		require.False(t, l.Now().IsZero())
		before, err := l.CountHeldPushEvents(ctx)
		require.NoError(t, err)
		require.Zero(t, before)
		require.NoError(t, l.HoldPushEvents(ctx, nil, "v"), "no holds is a no-op")
		require.NoError(t, l.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold("held-1"), pushHold("held-2")}, "v"))
		after, err := l.CountHeldPushEvents(ctx)
		require.NoError(t, err)
		require.Equal(t, 2, after, "the lock sees its own holds")
		creations, err := l.HeldPushCreations(ctx)
		require.NoError(t, err)
		require.Empty(t, creations, "holds with no creation event row are not creations")
		subjects, err := l.PushSubjects(ctx, []string{"held-1"})
		require.NoError(t, err)
		require.Empty(t, subjects)
		version, err := l.DataVersion(ctx)
		require.NoError(t, err)
		require.NotZero(t, version)
		return nil
	})
	require.NoError(t, err)
	n, err := s.CountHeldPushEvents(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n, "the holds are committed")
}

// TestWithPushLock_ErrorRollsBackHolds: an error from the locked function
// leaves no hold behind.
func TestWithPushLock_ErrorRollsBackHolds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	boom := errors.New("boom")

	err := s.WithPushLock(ctx, func(l sqlite.PushLocked) error {
		require.NoError(t, l.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold("held-1")}, "v"))
		return boom
	})
	require.ErrorIs(t, err, boom)
	n, err := s.CountHeldPushEvents(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

// openSecondStore opens a second connection to the database at path with a
// short busy timeout, so a write the lock keeps out fails at once.
func openSecondStore(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	other, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	_, err = other.WriteDB().ExecContext(context.Background(), `PRAGMA busy_timeout = 50`)
	require.NoError(t, err)
	return other
}

// otherWrite tries one small write from other.
func otherWrite(other *sqlite.Store) error {
	_, err := other.WriteDB().ExecContext(context.Background(),
		`UPDATE sync_events SET created_at = created_at WHERE 1 = 0`)
	return err
}

// TestWithPushLock_KeepsOtherWritersOut (MTIX-95.39): while the lock is held a
// write from another connection fails, and after it the same write succeeds.
func TestWithPushLock_KeepsOtherWritersOut(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lock.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	other := openSecondStore(t, path)

	require.NoError(t, s.WithPushLock(ctx, func(sqlite.PushLocked) error {
		require.Error(t, otherWrite(other), "the lock keeps the other connection out")
		return nil
	}))
	require.NoError(t, otherWrite(other), "the lock is released at commit")
}

// TestWithPushLock_OnCommitRunsOnlyWhenAHoldWasRecorded (MTIX-95.39): a locked
// attempt that records nothing changed nothing, so it asks for no export and
// dispatches no hook; one that records a hold does.
func TestWithPushLock_OnCommitRunsOnlyWhenAHoldWasRecorded(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	commits := 0
	s.SetOnCommit(func() { commits++ })

	require.NoError(t, s.WithPushLock(ctx, func(l sqlite.PushLocked) error {
		_, err := l.CountHeldPushEvents(ctx)
		return err
	}))
	require.NoError(t, s.WithPushLock(ctx, func(l sqlite.PushLocked) error {
		return l.HoldPushEvents(ctx, nil, "v")
	}))
	require.Zero(t, commits, "nothing recorded, nothing announced")

	require.NoError(t, s.WithPushLock(ctx, func(l sqlite.PushLocked) error {
		return l.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold("held-1")}, "v")
	}))
	require.Equal(t, 1, commits, "a recorded hold is announced once")
}

// TestWithPushLock_ThreeThousandHeldCreations_ReadsStayCheap (MTIX-95.39): with
// 3001 held creations the reread reads only the creations, without a payload,
// and the lock reads the same rows; the ticket's probe size, at the store
// level where a reread costs a single query.
func TestWithPushLock_ThreeThousandHeldCreations_ReadsStayCheap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < 3001)
			INSERT INTO sync_events (event_id, project_prefix, node_id, op_type, payload, wall_clock_ts,
			    lamport_clock, vector_clock, author_id, author_machine_hash, sync_status, created_at, uid)
			SELECT 'held-' || i, 'TEST', 'HELD-' || i, 'create_node', '{"title":"' || printf('%0300d', i) || '"}',
			    1, i, '{}', 'seed', 'seed', 'pending', '2026-01-01T00:00:00Z', 'held-' || i
			FROM seq`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < 3001)
			INSERT INTO sync_quarantine (event_id, source, raw_event, reason, first_seen, last_attempt, attempts, cli_version)
			SELECT 'held-' || i, 'push', '{}', 'too large: seeded', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 't'
			FROM seq`)
		return err
	}))

	creations, err := s.HeldPushCreations(ctx)
	require.NoError(t, err)
	require.Len(t, creations, 3001)
	for _, c := range creations {
		require.Empty(t, c.Payload)
		require.Equal(t, "create_node", c.OpType)
	}
	require.Equal(t, "held-1", creations[0].EventID, "queue order: Lamport clock first")
	require.NoError(t, s.WithPushLock(ctx, func(l sqlite.PushLocked) error {
		inLock, err := l.HeldPushCreations(ctx)
		require.NoError(t, err)
		require.Len(t, inLock, 3001, "the lock reads the same rows")
		return nil
	}))
}
