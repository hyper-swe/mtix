// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	syncpkg "github.com/hyper-swe/mtix/internal/sync"
	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/jackc/pgx/v5"
)

// SweepReport is the structured outcome of one Phase 1 dedup sweep
// (ADR-003 §7 Phase 1) for a single project.
type SweepReport struct {
	// Project is the project_prefix the sweep ran against.
	Project string `json:"project"`
	// Resolved is the number of LOSER create_node events renumbered in
	// this run (zero for a clean project, and zero on an idempotent
	// re-run because every loser is already recorded). It is NOT the
	// duplicate-group count: a group of N creates resolves to N-1 losers.
	Resolved int `json:"resolved"`
	// Remaps describes each renumber recorded in this run, loudly, so the
	// CLI can print exactly which numbers moved.
	Remaps []SweepRemap `json:"remaps,omitempty"`
}

// SweepRemap is one recorded renumber: the LOSER node (keyed by its stable
// uid, ADR-003 §2) that must move off a contested display_path, and the
// WINNER that kept the number (lowest event_id; first-create-wins).
type SweepRemap struct {
	LoserUID       string `json:"loser_uid"`
	OldDisplayPath string `json:"old_display_path"`
	WinnerEventID  string `json:"winner_event_id"`
	LoserEventID   string `json:"loser_event_id"`
}

// SweepDuplicates is the Phase 1 pre-constraint dedup sweep (ADR-003 §7
// Phase 1). The partial unique index (009) cannot be added to a log that
// already contains duplicate (project_prefix, display_path) create_node
// events — projects bitten by MTIX-28 before the index existed. This sweep
// makes such a log clean so Phase 1.5 can add the index.
//
// Behavior:
//   - Runs UNDER the hub's existing single-flight: it acquires
//     pg_advisory_xact_lock(hashtext(AdvisoryLockKey)) — the SAME lock
//     Migrate uses — at the top of its transaction, so N concurrent sweeps
//     serialize and exactly one resolves a given duplicate (the others
//     observe the now-clean state and resolve nothing).
//   - DETERMINISTIC tiebreak: for each duplicate (project, display_path)
//     group of DISTINCT logical nodes (distinct effective uid), the lowest
//     event_id keeps the number (first create / lowest id wins); every
//     other DISTINCT node is a loser. Re-running converges to the same
//     winner, which is what makes a crash mid-sweep resume-safe.
//   - SAME-logical-node duplicates (same effective uid — e.g. a --force
//     re-backfill that re-minted an event_id, MTIX-30.15) are NOT a
//     collision and are never renumbered.
//   - APPEND-ONLY: it NEVER UPDATEs an existing create_node row (the log
//     has no UPDATE trigger and must stay immutable, ADR-003 §13). It
//     records each loser's renumber in node_renumber_remaps (uid-keyed)
//     and a loud sync_conflicts row, both via INSERT.
//   - IDEMPOTENT: the remap table's uid PK makes re-recording a loser a
//     no-op (ON CONFLICT DO NOTHING), so a re-run resolves nothing new and
//     never double-renumbers.
//   - NO-OP for a clean project.
//
// Per ADR-003 §9 / docs/SECURITY-MODEL.md the sweep is a liveness
// mechanism, not a security boundary: it can at worst move a display
// number; it never loses or corrupts a node. Parameterized SQL only;
// errors redact any DSN.
func (p *Pool) SweepDuplicates(ctx context.Context, projectPrefix string) (SweepReport, error) {
	if p == nil || p.p == nil {
		return SweepReport{}, fmt.Errorf("SweepDuplicates: pool not open")
	}
	if projectPrefix == "" {
		return SweepReport{}, fmt.Errorf("SweepDuplicates: empty project prefix")
	}

	report := SweepReport{Project: projectPrefix}

	tx, err := p.p.Begin(ctx)
	if err != nil {
		return SweepReport{}, fmt.Errorf("sweep: begin: %s", redact.DSN(err.Error()))
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit; releases the lock on error

	// Single-flight: same advisory lock as Migrate, auto-released at
	// COMMIT/ROLLBACK so a crash never strands it.
	if _, lockErr := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`, AdvisoryLockKey,
	); lockErr != nil {
		return SweepReport{}, fmt.Errorf("sweep: acquire advisory lock: %s", redact.DSN(lockErr.Error()))
	}

	losers, err := findDuplicateLosers(ctx, tx, projectPrefix)
	if err != nil {
		return SweepReport{}, err
	}

	for _, l := range losers {
		recorded, err := recordRenumber(ctx, tx, projectPrefix, l)
		if err != nil {
			return SweepReport{}, err
		}
		if recorded {
			report.Resolved++
			report.Remaps = append(report.Remaps, l)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return SweepReport{}, fmt.Errorf("sweep: commit: %s", redact.DSN(err.Error()))
	}
	return report, nil
}

// PreviewDuplicates counts how many loser create_node events the Phase 1
// sweep WOULD renumber for the project, WITHOUT recording anything. It is
// the read-only dry-run path behind `mtix sync migrate` (no --yes): it
// runs the same duplicate-detection query as SweepDuplicates but never
// inserts a remap or conflict and takes no advisory lock (a pure read).
//
// Liveness, not a security boundary (ADR-003 §9). Parameterized SQL;
// errors redact any DSN.
func (p *Pool) PreviewDuplicates(ctx context.Context, projectPrefix string) (int, error) {
	if p == nil || p.p == nil {
		return 0, fmt.Errorf("PreviewDuplicates: pool not open")
	}
	if projectPrefix == "" {
		return 0, fmt.Errorf("PreviewDuplicates: empty project prefix")
	}
	tx, err := p.p.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("preview: begin: %s", redact.DSN(err.Error()))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	losers, err := findDuplicateLosers(ctx, tx, projectPrefix)
	if err != nil {
		return 0, err
	}
	if len(losers) == 0 {
		return 0, nil
	}

	// Subtract losers already recorded in a prior sweep so the preview
	// matches what a subsequent --yes run would actually resolve. One
	// set-based query (the loser uids vs the remap ledger) rather than a
	// per-loser probe.
	uids := make([]string, len(losers))
	for i, l := range losers {
		uids[i] = l.LoserUID
	}
	var alreadyRecorded int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM node_renumber_remaps WHERE uid = ANY($1)`, uids,
	).Scan(&alreadyRecorded); err != nil {
		return 0, fmt.Errorf("preview: probe remaps: %s", redact.DSN(err.Error()))
	}
	return len(losers) - alreadyRecorded, nil
}

// findDuplicateLosers returns the loser create_node of every duplicate
// (project, display_path) group: the group's distinct logical nodes
// (distinct effective uid) minus the winner (lowest event_id). Same-uid
// re-mints collapse to one logical node and never produce a loser
// (ADR-003 §2/§6, MTIX-30.15).
//
// The winner per group is the row with the lowest event_id among that
// group's DISTINCT uids; every other distinct uid's lowest-event_id row is
// a loser. event_id is a UUIDv7 (time-ordered), so lowest event_id is the
// first create — replica-consistent and deterministic across re-runs.
func findDuplicateLosers(ctx context.Context, tx pgx.Tx, prefix string) ([]SweepRemap, error) {
	// One canonical row per (display_path, logical-node): the lowest
	// event_id for each distinct effective uid. effective uid = COALESCE
	// of the stored uid (NULL/'' falls back to the row's own event_id,
	// ADR-003 §2). Then, within each display_path, rank logical nodes by
	// their canonical event_id; rank 1 keeps the number, the rest lose.
	rows, err := tx.Query(ctx, `
		WITH creates AS (
		    SELECT
		        node_id AS display_path,
		        CASE WHEN uid IS NULL OR uid = '' THEN event_id ELSE uid END AS eff_uid,
		        event_id
		    FROM sync_events
		    WHERE project_prefix = $1 AND op_type = 'create_node'
		),
		per_node AS (
		    -- collapse same-logical-node re-mints to the first create
		    SELECT display_path, eff_uid, min(event_id) AS node_event_id
		    FROM creates
		    GROUP BY display_path, eff_uid
		),
		ranked AS (
		    SELECT display_path, eff_uid, node_event_id,
		        row_number() OVER (
		            PARTITION BY display_path ORDER BY node_event_id
		        ) AS rnk,
		        first_value(node_event_id) OVER (
		            PARTITION BY display_path ORDER BY node_event_id
		        ) AS winner_event_id
		    FROM per_node
		)
		SELECT eff_uid, display_path, node_event_id, winner_event_id
		FROM ranked
		WHERE rnk > 1
		ORDER BY display_path, node_event_id`,
		prefix,
	)
	if err != nil {
		return nil, fmt.Errorf("sweep: scan duplicates: %s", redact.DSN(err.Error()))
	}
	defer rows.Close()

	var out []SweepRemap
	for rows.Next() {
		var r SweepRemap
		if err := rows.Scan(&r.LoserUID, &r.OldDisplayPath, &r.LoserEventID, &r.WinnerEventID); err != nil {
			return nil, fmt.Errorf("sweep: scan row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sweep: rows: %w", err)
	}
	return out, nil
}

// recordRenumber records one loser's renumber: a uid-keyed row in
// node_renumber_remaps (the canonical, idempotent ledger) and — only when
// that row is newly inserted — a loud sync_conflicts row. Returns whether
// a NEW renumber was recorded (false on an idempotent re-run where the
// loser is already present). APPEND-ONLY: never touches the create rows.
func recordRenumber(ctx context.Context, tx pgx.Tx, prefix string, r SweepRemap) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO node_renumber_remaps
		  (uid, project_prefix, old_display_path, loser_event_id, winner_event_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (uid) DO NOTHING`,
		r.LoserUID, prefix, r.OldDisplayPath, r.LoserEventID, r.WinnerEventID,
	)
	if err != nil {
		return false, fmt.Errorf("sweep: record remap %s: %s", r.LoserUID, redact.DSN(err.Error()))
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already recorded ⇒ idempotent no-op
	}

	// Surface loudly via the existing conflicts path (resolution 'manual':
	// the sweep is an operator-visible migration action, not an automatic
	// LWW). field_name 'display_path' marks this as a number move.
	if _, err := tx.Exec(ctx, `
		INSERT INTO sync_conflicts
		  (event_id_a, event_id_b, node_id, field_name, resolution, resolved_by)
		VALUES ($1, $2, $3, 'display_path', 'manual', 'phase1-sweep')`,
		r.WinnerEventID, r.LoserEventID, r.OldDisplayPath,
	); err != nil {
		return false, fmt.Errorf("sweep: surface conflict %s: %s", r.LoserUID, redact.DSN(err.Error()))
	}
	return true, nil
}

// IndexResult is the structured outcome of EnsureRegistryIndex (Phase 1.5,
// ADR-003 §7 Phase 1.5 / audit F-4).
type IndexResult struct {
	// GateOpen reports whether every active client meets
	// sync.UIDKeyedMinVersion (the version gate). When false the index is
	// deferred, not added.
	GateOpen bool `json:"gate_open"`
	// Added reports whether THIS call created the index. False when the
	// gate is closed (deferred) or the index already existed (idempotent).
	Added bool `json:"added"`
	// CreateCount is the number of create_node rows scanned for the loud
	// pre-add report.
	CreateCount int `json:"create_count"`
}

// EnsureRegistryIndex is Phase 1.5 (ADR-003 §7 Phase 1.5 / audit F-4): the
// VERSION-GATED add of the partial unique index that backs the node-number
// registry (009).
//
// It adds the index only when ProjectAllClientsAtLeast(project,
// sync.UIDKeyedMinVersion) is true — i.e. every active client understands
// renumber/remap events. Until then the add is DEFERRED (GateOpen=false,
// Added=false): emitting the index early would hard-error or silently
// diverge an older CLI that pushes a now-renumbered number (ADR-003 §7).
//
// Phase 1 MUST precede Phase 1.5: adding a UNIQUE index to a log that still
// contains duplicate (project_prefix, display_path) create events
// hard-errors. We rely on that — the CREATE returns a unique-violation that
// this method surfaces loudly — and the docstring states the precondition.
//
// CONCURRENTLY-vs-transaction tension (build-plan hazard a):
// CREATE UNIQUE INDEX CONCURRENTLY cannot run inside a transaction, which
// conflicts with Migrate's single-tx model. We resolve it by running the
// index build OUTSIDE any transaction, on a dedicated single connection,
// guarded by a SESSION-level pg_advisory_lock(hashtext(AdvisoryLockKey)) —
// the same single-flight key, explicitly unlocked in a defer (a session
// lock is NOT auto-released at statement end the way an xact lock is). The
// index is the one declared in 009 (sync_events_node_registry_uidx) so the
// online build and the migration converge on the identical object; IF NOT
// EXISTS keeps a re-run idempotent.
//
// Parameterized SQL where values are bound; the index DDL is static.
// Liveness, not a security boundary (ADR-003 §9): a closed gate only defers.
func (p *Pool) EnsureRegistryIndex(ctx context.Context, projectPrefix string) (IndexResult, error) {
	if p == nil || p.p == nil {
		return IndexResult{}, fmt.Errorf("EnsureRegistryIndex: pool not open")
	}
	if projectPrefix == "" {
		return IndexResult{}, fmt.Errorf("EnsureRegistryIndex: empty project prefix")
	}

	gateOpen, err := p.ProjectAllClientsAtLeast(ctx, projectPrefix, syncpkg.UIDKeyedMinVersion)
	if err != nil {
		return IndexResult{}, fmt.Errorf("EnsureRegistryIndex: version gate: %w", err)
	}
	res := IndexResult{GateOpen: gateOpen}
	if !gateOpen {
		// Deferred behind the gate — the loud pre-add report is the
		// caller's job; here we just signal not-added.
		return res, nil
	}

	// Loud pre-add report input: how many create rows the index will cover.
	if countErr := p.p.QueryRow(ctx, `
		SELECT count(*) FROM sync_events
		WHERE project_prefix = $1 AND op_type = 'create_node'`,
		projectPrefix,
	).Scan(&res.CreateCount); countErr != nil {
		return IndexResult{}, fmt.Errorf("EnsureRegistryIndex: pre-add count: %s", redact.DSN(countErr.Error()))
	}

	// Acquire a dedicated connection so the session advisory lock and the
	// non-tx CREATE INDEX CONCURRENTLY share one backend.
	conn, err := p.p.Acquire(ctx)
	if err != nil {
		return IndexResult{}, fmt.Errorf("EnsureRegistryIndex: acquire conn: %s", redact.DSN(err.Error()))
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx,
		`SELECT pg_advisory_lock(hashtext($1))`, AdvisoryLockKey,
	); err != nil {
		return IndexResult{}, fmt.Errorf("EnsureRegistryIndex: lock: %s", redact.DSN(err.Error()))
	}
	defer func() {
		// Session lock is NOT auto-released; unlock on the same conn.
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, AdvisoryLockKey)
	}()

	// Re-check existence under the lock so two racers don't both report
	// Added=true. pg_class lookup of the named index.
	var existed bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM pg_class WHERE relname = 'sync_events_node_registry_uidx'
		)`).Scan(&existed); err != nil {
		return IndexResult{}, fmt.Errorf("EnsureRegistryIndex: index probe: %s", redact.DSN(err.Error()))
	}
	if existed {
		return res, nil // idempotent: already present
	}

	// CONCURRENTLY: non-blocking online build, OUTSIDE any transaction
	// (the dedicated conn is in autocommit). A still-dirty log makes this
	// fail with a unique violation — surfaced loudly, never swallowed.
	if _, err := conn.Exec(ctx, `
		CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS sync_events_node_registry_uidx
		    ON sync_events (project_prefix, node_id)
		    WHERE op_type = 'create_node'`,
	); err != nil {
		return IndexResult{}, fmt.Errorf(
			"EnsureRegistryIndex: build index (Phase 1 must run first if duplicates remain): %s",
			redact.DSN(err.Error()))
	}
	res.Added = true
	return res, nil
}
