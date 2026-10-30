// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/model"
)

// SetAfterCommitForTest sets the push commit seam of p (MTIX-95.3, review
// F-80): hook runs after each push transaction's COMMIT succeeded, and a
// non-nil error it returns is reported in place of the commit's, as a lost
// commit acknowledgement would be. Compiled into tests only; the seam is a
// field of this Pool, never package state.
func SetAfterCommitForTest(p *Pool, hook func() error) {
	p.afterCommit = hook
}

// SetAfterSessionLockForTest sets the session-lock seam of p (MTIX-107.85):
// hook runs right after EnsureRegistryIndex takes its session advisory
// lock. Compiled into tests only; the seam is a field of this Pool, never
// package state.
func SetAfterSessionLockForTest(p *Pool, hook func()) {
	p.afterSessionLock = hook
}

// ReleaseUnheldSessionLockForTest runs the session-lock release on a
// pooled connection that does not hold the lock, so the unlock returns
// false (MTIX-107.85). It returns whether the connection is closed
// afterwards and the release's error.
func ReleaseUnheldSessionLockForTest(ctx context.Context, p *Pool) (bool, error) {
	conn, err := p.p.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	relErr := releaseSessionLock(ctx, conn)
	return conn.Conn().IsClosed(), relErr
}

// PushOneEventForTest runs one event through the push path of a zero Pool on
// tx, as PushEventsResult does inside its transaction, so tests can drive
// the per-event error paths with a scripted transaction (MTIX-187.4.4).
func PushOneEventForTest(ctx context.Context, tx pgx.Tx, e *model.SyncEvent) error {
	acc := &pushAccum{
		seenPrefixes: map[string]struct{}{},
		batchClaims:  map[registryKey]batchClaim{},
	}
	return (&Pool{}).pushOneEvent(ctx, tx, e, acc)
}

// RecordCollisionForTest runs the restore-collision recorder on tx
// (MTIX-187.4.4).
func RecordCollisionForTest(ctx context.Context, tx pgx.Tx, rc *RestoreCollision) error {
	return recordCollision(ctx, tx, rc, "uid", 1)
}

// PersistConflictsForTest runs the conflict writer on tx (MTIX-187.4.4).
func PersistConflictsForTest(ctx context.Context, tx pgx.Tx, conf []ConflictDescriptor) error {
	return persistConflicts(ctx, tx, conf)
}

// ElsewhereRefusalForTest returns the registry-index refusal for duplicate
// creates recorded at another number (MTIX-187.4.4).
func ElsewhereRefusalForTest(eventIDs []string) error { return elsewhereRefusal(eventIDs) }

// SetBeforeMigrateCommitForTest sets p's before-commit seam (FR-18.4).
// Tests set it before calling Migrate, never concurrently with Migrate.
// Compiled into tests only; the hook belongs to this Pool, not package state.
func SetBeforeMigrateCommitForTest(p *Pool, hook func(pgx.Tx) error) {
	p.beforeMigrateCommit = hook
}
