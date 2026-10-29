// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"strings"

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
//
// Locking: migrations 010 and 013 check pg_attribute before ALTER, so
// existing sync_events columns take no ACCESS EXCLUSIVE lock. Adding a
// missing column still blocks pushes and pulls until commit. Ordinary index
// builds (014/015) take SHARE locks until commit, blocking writers but not
// SELECTs unless an earlier schema change holds a stronger lock. Trigger
// repair can also block reads/writes to its table; 006 skips unchanged,
// enabled triggers, avoiding exclusive relation locks on a current hub. The
// caller's context bounds both the advisory-lock wait and the schema transaction.
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

	// Migration is a known long-running, single-flight operation: a waiter can
	// legitimately block on the advisory lock while another node runs the full
	// schema migration, and over cloud network latency that easily exceeds the
	// default per-statement timeout the pool applies to normal ops (MTIX-48).
	// Disable statement_timeout for THIS transaction only (SET LOCAL reverts at
	// commit/rollback, leaving the pooled connection's default intact); the
	// caller's context deadline still bounds the whole operation, since pgx
	// cancels on ctx regardless of statement_timeout.
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = 0`); err != nil {
		return fmt.Errorf("migrate: relax statement_timeout: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`, AdvisoryLockKey,
	); err != nil {
		return fmt.Errorf("migrate: acquire advisory lock: %w", err)
	}

	// Refuse before creating anything when the search_path would put new
	// objects away from existing sync tables (MTIX-95.7).
	if err := checkSchemaFirst(ctx, tx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Apply all migration files in a SINGLE round-trip. Every file is
	// idempotent (IF NOT EXISTS), so re-running the whole set is always correct
	// — even after an out-of-band schema change, where a version table would
	// desync and silently skip recreating a dropped object. Collapsing ~13
	// round-trips into one keeps a re-run fast, which is what lets concurrent
	// callers that serialize on the single-flight lock finish within a sane
	// deadline over cloud network latency (MTIX-48).
	// SQL Rule 1a permits constant DDL from embedded migration files;
	// only compiled-in bodies and a constant separator form this statement.
	var combined strings.Builder
	for _, name := range files {
		body, err := migrations.Read(name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		combined.WriteString(body)
		combined.WriteString("\n;\n")
	}
	if _, err := tx.Exec(ctx, combined.String()); err != nil {
		return fmt.Errorf("migrate: apply schema: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrate: commit: %w", err)
	}
	return nil
}
