// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

const cloneWakeBootstrapKey = "meta.sync.clone.wake_bootstrap"

type cloneWakePosition struct {
	generation, eventID string
	tail                int64
}

func (p cloneWakePosition) witness() string {
	return fmt.Sprintf("%q/%d/%q", p.generation, p.tail, p.eventID)
}

// WithCloneWakeBootstrap applies a clone batch and seeds its floors atomically
// (FR-18.20, FR-20; MTIX-112.2.2.1.4). Only a stream begun in an actually empty
// journal may seed. Its durable tail identity survives interruption; any other
// journal writer invalidates it, so fresh local mail is never covered later.
func (s *Store) WithCloneWakeBootstrap(ctx context.Context, fn func(*sql.Tx) error) error {
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		before, err := cloneWakePositionTx(ctx, tx)
		if err != nil {
			return err
		}
		seed, err := continueCloneWakeBootstrapTx(ctx, tx, before)
		if err != nil {
			return err
		}
		if applyErr := fn(tx); applyErr != nil {
			return applyErr
		}
		if !seed {
			return nil
		}
		if floorErr := initHookScanFloorAtTailTx(ctx, tx, true); floorErr != nil {
			return floorErr
		}
		after, err := cloneWakePositionTx(ctx, tx)
		if err != nil {
			return err
		}
		// The witness commits with its batch and floors, never before them.
		_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES (?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, cloneWakeBootstrapKey, after.witness())
		return err
	})
	if err != nil {
		return fmt.Errorf("clone wake bootstrap: %w", err)
	}
	return nil
}

func cloneWakePositionTx(ctx context.Context, tx *sql.Tx) (cloneWakePosition, error) {
	var p cloneWakePosition
	// Generation fences discard; exact tail identity fences replace/reused seqs.
	err := tx.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT value FROM meta WHERE key='meta.sync.journal_generation'),'0'),
		COALESCE(MAX(rowid),0),
		COALESCE((SELECT event_id FROM sync_events ORDER BY rowid DESC LIMIT 1),'')
		FROM sync_events`).Scan(&p.generation, &p.tail, &p.eventID)
	if err != nil {
		return p, fmt.Errorf("read clone journal witness: %w", err)
	}
	return p, nil
}

func continueCloneWakeBootstrapTx(ctx context.Context, tx *sql.Tx, before cloneWakePosition) (bool, error) {
	var saved string
	// An absent witness on a nonempty journal never grants bootstrap authority.
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM meta WHERE key=?),'')`, cloneWakeBootstrapKey).Scan(&saved)
	if err != nil {
		return false, fmt.Errorf("read clone wake bootstrap: %w", err)
	}
	if before.tail == 0 || saved == before.witness() {
		return true, nil
	}
	if saved != "" {
		// Keep invalidation durable across every later nonempty clone batch.
		if err := CompleteCloneWakeBootstrap(ctx, tx); err != nil {
			return false, err
		}
	}
	return false, nil
}

// CompleteCloneWakeBootstrap clears clone intent in the successful final
// pull-cursor transaction, so repeat resumes preserve later live mail (FR-20).
func CompleteCloneWakeBootstrap(ctx context.Context, tx *sql.Tx) error {
	// Completion and invalidation both retire the same local-only witness.
	if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, cloneWakeBootstrapKey); err != nil {
		return fmt.Errorf("clear clone wake bootstrap: %w", err)
	}
	return nil
}
