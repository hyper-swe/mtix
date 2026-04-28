// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// AdvisoryLockKey is the input string passed through hashtext() to
// produce the PG advisory-lock id used by Migrate. It MUST stay
// "mtix_sync_migration" verbatim — every mtix CLI hashes the same
// string, so changing it would be a breaking protocol change per
// FR-18.14.
const AdvisoryLockKey = "mtix_sync_migration"

// Migrate runs every embedded migration file under
// internal/store/postgres/migrations in lexical order, inside a single
// PG transaction guarded by pg_advisory_xact_lock(hashtext(AdvisoryLockKey)).
//
// Single-flight guarantee: N concurrent CLIs first-connecting to a
// fresh hub all call Migrate; exactly one runs the SQL while the
// others block on the advisory lock. Once the leader commits, the
// others acquire the lock, observe the now-committed schema (CREATE
// TABLE IF NOT EXISTS = no-ops), and commit immediately.
//
// Crash safety: pg_advisory_xact_lock is auto-released by PG at
// COMMIT or ROLLBACK. A crashed CLI mid-migration leaves the
// transaction rolled back AND the lock released. The next CLI re-runs
// the whole migration cleanly — the partial-migration recovery test
// asserts this property.
func (p *Pool) Migrate(ctx context.Context) error {
	if p == nil || p.p == nil {
		return fmt.Errorf("migrate: pool not open")
	}

	files, err := migrations.Files()
	if err != nil {
		return fmt.Errorf("migrate: list files: %w", err)
	}

	tx, err := p.p.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrate: begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx) // no-op after Commit; releases the advisory lock on error
	}()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`, AdvisoryLockKey,
	); err != nil {
		return fmt.Errorf("migrate: acquire advisory lock: %w", err)
	}

	for _, name := range files {
		body, err := migrations.Read(name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, body); err != nil {
			return fmt.Errorf("migrate: exec %s: %w", name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrate: commit: %w", err)
	}
	return nil
}
