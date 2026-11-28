// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// Bounded-memory import, slice 2 (MTIX-2.3.2.2, FR-7.8). PlanImportStaged
// decides what ImportReconcile decides (uid collisions, force-renames,
// renumbers, moves, uid adoptions, title mismatches, workflow conflicts) from
// the staged incoming rows (MTIX-2.3.2.1) and the local records, with indexed
// queries: the node lists live in tables of the staging database, never in
// slices. What it plans is kept there too, as rows of the report, and is
// streamed out by WriteReport and WriteRemap. Nothing is written to the store.

// planQuerier is the part of *sql.Tx and *sql.DB the plan reads and writes through.
type planQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// planSchemaSQL creates the plan tables: plan_rows holds the report lists
// (columns as reportRow, see reportSection), plan_in the incoming nodes with
// the id, uid, parent and number the reconcile rewrites them to (f_*), plan_local
// the local nodes with the id they end at, plan_taken the numbers the plan took,
// plan_rebase the renumbered provisional ids, plan_order the order they are
// renumbered in, and plan_remap the pairs of the remap file.
const planSchemaSQL = `
CREATE TABLE plan_rows (sec INTEGER NOT NULL, ord INTEGER NOT NULL, c1 TEXT NOT NULL, c2 TEXT NOT NULL,
	c3 TEXT NOT NULL, c4 TEXT NOT NULL, c5 TEXT NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (sec, ord)) WITHOUT ROWID;
CREATE INDEX plan_rows_c3 ON plan_rows (sec, c3);
CREATE TABLE plan_in (ord INTEGER PRIMARY KEY, id TEXT NOT NULL, uid TEXT NOT NULL, parent_id TEXT NOT NULL,
	project TEXT NOT NULL, depth INTEGER NOT NULL, seq INTEGER NOT NULL, created_at TEXT NOT NULL,
	title TEXT NOT NULL, prov INTEGER NOT NULL, f_id TEXT NOT NULL, f_uid TEXT NOT NULL,
	f_parent TEXT NOT NULL, f_seq INTEGER NOT NULL);
CREATE INDEX plan_in_id ON plan_in (id, ord);
CREATE INDEX plan_in_uid ON plan_in (uid, ord) WHERE uid <> '';
CREATE INDEX plan_in_fuid ON plan_in (f_uid, ord) WHERE f_uid <> '';
CREATE INDEX plan_in_parent ON plan_in (project, parent_id);
CREATE TABLE plan_local (pos INTEGER PRIMARY KEY, id TEXT NOT NULL, uid0 TEXT NOT NULL, uid TEXT NOT NULL,
	project TEXT NOT NULL, parent_id TEXT NOT NULL, seq INTEGER NOT NULL, created_at TEXT NOT NULL,
	title TEXT NOT NULL, stamp INTEGER NOT NULL, final TEXT NOT NULL, final_seq INTEGER NOT NULL,
	renumbered INTEGER NOT NULL);
CREATE INDEX plan_local_id ON plan_local (id);
CREATE INDEX plan_local_final ON plan_local (final);
CREATE INDEX plan_local_parent ON plan_local (parent_id);
CREATE TABLE plan_taken (k TEXT NOT NULL, seq INTEGER NOT NULL, PRIMARY KEY (k, seq)) WITHOUT ROWID;
CREATE TABLE plan_hint (k TEXT PRIMARY KEY, next INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE plan_rebase (old TEXT PRIMARY KEY, new TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE plan_order (pos INTEGER PRIMARY KEY, ord INTEGER NOT NULL);
CREATE TABLE plan_remap (k INTEGER PRIMARY KEY, uid TEXT NOT NULL, path TEXT NOT NULL);
CREATE INDEX plan_remap_uid ON plan_remap (uid, k);`

// ImportPlan is the reconciliation of a staged export against the store, kept
// in the staging database (see PlanImportStaged). It is a ReportSource. It is
// valid until the StagedExport it was planned from is closed.
type ImportPlan struct {
	s    *Store
	st   *StagedExport
	opts ImportReconcileOptions
	q    planQuerier // the planning transaction while planning, the staging database after

	counts       [secCount]int
	localCount   int
	idempotent   int
	wfIdempotent int  // workflow conflicts the reconcile had counted as no-ops
	wfShown      bool // the workflow conflicts are part of the report (refused or applied)
	applied      bool
}

// PlanImportStaged plans the reconciliation of the staged export with the
// store, deciding what ImportReconcile decides (ADR-003 §6, FR-7.8), in the
// same order and with the same errors, in memory bounded by one node: the
// result is the same report. The plan is returned, never nil, even with an
// error (as ImportReconcile returns its report), so the loud report of a
// rejected or withheld import can be written. It writes nothing to the store.
func (s *Store) PlanImportStaged(ctx context.Context, st *StagedExport, opts ImportReconcileOptions) (*ImportPlan, error) {
	if st == nil {
		return nil, fmt.Errorf("staged import is nil: %w", model.ErrInvalidInput)
	}
	p := &ImportPlan{s: s, st: st, opts: opts}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return p, fmt.Errorf("begin import plan: %w", err)
	}
	p.q = tx
	planErr := p.run(ctx)
	p.q = st.db
	if err := tx.Commit(); err != nil {
		return p, errors.Join(planErr, fmt.Errorf("commit import plan: %w", err))
	}
	return p, planErr
}

// run plans in the order ImportReconcile does.
func (p *ImportPlan) run(ctx context.Context) error {
	if _, err := p.q.ExecContext(ctx, planSchemaSQL); err != nil {
		return fmt.Errorf("create plan tables: %w", err)
	}
	steps := []func(context.Context) error{
		p.loadIncoming, p.validateUIDs, p.planLocalRenumbers, p.rejectConflicts,
		p.planProvisionalRemaps, p.planUIDAdoptions, p.requireConfirmation, p.planWorkflow,
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

// rejectConflicts rejects the import, as rejectConflicts does for a report.
func (p *ImportPlan) rejectConflicts(_ context.Context) error {
	if n := p.counts[secConflict]; n > 0 {
		return fmt.Errorf("import rejected: %d uid conflict(s): %w", n, model.ErrConflict)
	}
	return nil
}

// MarkApplied records that the planned import was committed: the report then
// says applied, and lists the workflow choices that were made.
func (p *ImportPlan) MarkApplied() { p.applied = true }

// IsApplied reports whether the import was committed.
func (p *ImportPlan) IsApplied() bool { return p.applied }

// add appends one entry to a report list.
func (p *ImportPlan) add(ctx context.Context, sec reportSection, r reportRow) error {
	// One entry of a report list, numbered after the entries before it.
	if _, err := p.q.ExecContext(ctx,
		`INSERT INTO plan_rows (sec, ord, c1, c2, c3, c4, c5, n) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		int(sec), p.counts[sec], r.c[0], r.c[1], r.c[2], r.c[3], r.c[4], r.n); err != nil {
		return fmt.Errorf("record plan entry: %w", err)
	}
	p.counts[sec]++
	return nil
}

// addRemap appends a remap-shaped entry (uid, old path, new path).
func (p *ImportPlan) addRemap(ctx context.Context, sec reportSection, uid, oldPath, newPath string, minted bool) error {
	r := reportRow{c: [5]string{uid, oldPath, newPath}}
	if minted {
		r.n = 1
	}
	return p.add(ctx, sec, r)
}

// take records number seq as taken under the parent key (see takenKey).
func (p *ImportPlan) take(ctx context.Context, key string, seq int) error {
	// A number the plan took under a parent.
	if _, err := p.q.ExecContext(ctx, `INSERT OR IGNORE INTO plan_taken (k, seq) VALUES (?, ?)`, key, seq); err != nil {
		return fmt.Errorf("record a taken number: %w", err)
	}
	return nil
}

// isTaken reports whether the plan took number seq under the parent key.
func (p *ImportPlan) isTaken(ctx context.Context, key string, seq int) (bool, error) {
	var one int
	// Whether the plan took this number under this parent.
	err := p.q.QueryRowContext(ctx, `SELECT 1 FROM plan_taken WHERE k = ? AND seq = ?`, key, seq).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read a taken number: %w", err)
	}
	return true, nil
}
