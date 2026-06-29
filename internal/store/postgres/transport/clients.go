// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"time"

	syncpkg "github.com/hyper-swe/mtix/internal/sync"
	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ClientActiveWindow scopes which sync_project_clients rows count toward
// the version gate. A client whose last_seen_at is older than this is
// treated as departed and is NOT allowed to hold the migration gate
// closed forever. The window is generous (a CLI that has not pushed in
// 30 days is assumed off the project) so the gate does not open while a
// merely-quiet-but-still-present client is mid-upgrade.
//
// "Active" is defined as last_seen_at >= now() - ClientActiveWindow.
// Future-skewed timestamps (last_seen_at > now()) are trivially inside
// the window and so are counted — a clock-skewed client is still a real
// client and must not be silently dropped from the gate.
const ClientActiveWindow = 30 * 24 * time.Hour

// pgExecutor is satisfied by both *pgxpool.Pool and pgx.Tx, letting the
// per-client upsert run either standalone (sync init) or inside an
// existing push transaction without duplicating the SQL.
type pgExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// upsertProjectClientExec upserts one (project, machine) row recording
// the calling CLI's version and now() as last_seen_at. It is the shared
// implementation behind both Pool.UpsertProjectClient (standalone) and
// the in-transaction call from pushEventsOnce.
//
// Backs the real version-negotiation gate of ADR-003 §7 Phase 1.5/3:
// the pre-existing sync_projects.last_seen_cli_version is a single
// last-writer value and cannot express "every CLI is compatible"; this
// per-client row can.
//
// Parameterized SQL only (SECURITY-MODEL: bind every value). last_seen
// is set server-side to now() so a skewed client clock cannot forge an
// arbitrarily-old timestamp to dodge the gate.
func upsertProjectClientExec(ctx context.Context, db pgExecutor, prefix, machineHash, cliVersion string) error {
	if prefix == "" || machineHash == "" || cliVersion == "" {
		return fmt.Errorf("upsert project client: prefix, machineHash, cliVersion all required")
	}
	_, err := db.Exec(ctx, `
		INSERT INTO sync_project_clients (project_prefix, machine_hash, cli_version, last_seen_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (project_prefix, machine_hash)
		DO UPDATE SET cli_version = EXCLUDED.cli_version, last_seen_at = now()`,
		prefix, machineHash, cliVersion,
	)
	if err != nil {
		return fmt.Errorf("upsert project client: %s", redact.DSN(err.Error()))
	}
	return nil
}

// UpsertProjectClient records the calling CLI's (project, machine,
// version, now()) on the hub for the version-negotiation gate. Called on
// sync init (cmd/mtix/sync_init.go) so a CLI registers its version even
// before its first push.
//
// Idempotent: re-calling for the same (project, machine) updates the
// version and last_seen_at in place via the primary-key upsert.
func (p *Pool) UpsertProjectClient(ctx context.Context, prefix, machineHash, cliVersion string) error {
	if p == nil || p.p == nil {
		return fmt.Errorf("UpsertProjectClient: pool not open")
	}
	return upsertProjectClientExec(ctx, p.p, prefix, machineHash, cliVersion)
}

// ProjectAllClientsAtLeast reports whether every *active* client of the
// project (last_seen_at within ClientActiveWindow) runs a CLI version at
// or above minVersion. It is the gate ADR-003 §7 Phase 1.5/3 consumes
// before adding the partial unique index or cutting over to UID-keyed
// events.
//
// Returns false (gate CLOSED) when:
//   - no active client is registered (we cannot prove "every CLI is
//     compatible" when we know of none), or
//   - any active client's stored cli_version is below minVersion, or
//   - any active client's stored cli_version is unparseable (fail safe —
//     an unknown version is treated as not-meeting the minimum).
//
// Per ADR-003 §9 / docs/SECURITY-MODEL.md this is a liveness mechanism,
// not a security boundary: a false (closed) result only defers a
// migration; it never loses or corrupts data.
//
// Parameterized SQL; the active-window cutoff is a bound parameter.
func (p *Pool) ProjectAllClientsAtLeast(ctx context.Context, projectPrefix, minVersion string) (bool, error) {
	if p == nil || p.p == nil {
		return false, fmt.Errorf("ProjectAllClientsAtLeast: pool not open")
	}
	if projectPrefix == "" {
		return false, fmt.Errorf("ProjectAllClientsAtLeast: empty project prefix")
	}
	// Validate the caller's minVersion up front so a caller bug surfaces
	// as an error rather than a silently-closed gate.
	if _, err := syncpkg.ParseVersion(minVersion); err != nil {
		return false, fmt.Errorf("ProjectAllClientsAtLeast: bad minVersion: %w", err)
	}

	cutoff := time.Now().UTC().Add(-ClientActiveWindow)
	rows, err := p.p.Query(ctx, `
		SELECT cli_version
		FROM sync_project_clients
		WHERE project_prefix = $1
		  AND last_seen_at >= $2`,
		projectPrefix, cutoff,
	)
	if err != nil {
		return false, fmt.Errorf("ProjectAllClientsAtLeast query: %s", redact.DSN(err.Error()))
	}
	defer rows.Close()

	seenAny := false
	for rows.Next() {
		var version string
		if scanErr := rows.Scan(&version); scanErr != nil {
			return false, fmt.Errorf("ProjectAllClientsAtLeast scan: %w", scanErr)
		}
		seenAny = true
		ok, cmpErr := syncpkg.AtLeast(version, minVersion)
		if cmpErr != nil {
			// Fail safe: an unparseable stored version cannot be proven
			// compatible, so the gate stays closed.
			return false, nil
		}
		if !ok {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("ProjectAllClientsAtLeast rows: %w", err)
	}

	// No active client ⇒ cannot prove every CLI is compatible ⇒ closed.
	return seenAny, nil
}

// recordClientOnPush upserts the calling CLI's client row inside an
// existing push transaction, using the project prefix of the batch. A
// no-op when no client identity is set or the batch has no project. Used
// by pushEventsOnce so every push refreshes the gate's view.
//
// Compile-time guard that the standalone executor (*pgxpool.Pool) shares
// the Exec shape pgExecutor relies on; pgx.Tx is itself an interface
// passed at the call site below.
var _ pgExecutor = (*pgxpool.Pool)(nil)

func (p *Pool) recordClientOnPush(ctx context.Context, tx pgx.Tx, projectPrefix string) error {
	if p == nil || p.clientMachineHash == "" || p.clientCLIVersion == "" || projectPrefix == "" {
		return nil
	}
	return upsertProjectClientExec(ctx, tx, projectPrefix, p.clientMachineHash, p.clientCLIVersion)
}
