// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// The prune notice is once per database (MTIX-95.16), kept in meta:
//   - vcPrunePendingKey is written by notePrune, inside the pruning
//     transaction, when the clock is pruned and no notice was ever issued;
//   - vcPrunedKey (RFC 3339 UTC) is written by Store.reportVCPrune after the
//     commit, with the Store's clock, together with the one Warn through the
//     Store's injected logger. Its presence means "already warned".
//
// notePrune runs in free functions (emitEvent, IdempotentApply) that have
// neither the Store's logger nor its clock, so it only marks; Store.WithTx,
// which every write goes through, reports. No package-level state.
const (
	vcPrunePendingKey = "sync.vc_prune_pending"
	vcPrunedKey       = "sync.vc_pruned_first_at"
)

// vcLogger returns the logger for vector-clock state warnings raised in
// free functions that have no Store: slog.Default.
func vcLogger(_ context.Context) *slog.Logger { return slog.Default() }

// notePrune marks, in the caller's transaction, that the clock was pruned,
// unless the database already issued its notice. It does nothing when
// pruned is 0. A failure is logged at debug level and never fails the
// write: the notice must not break the invariant it reports on.
func notePrune(ctx context.Context, tx *sql.Tx, pruned int, trigger string) {
	if pruned == 0 {
		return
	}
	// Mark the first prune only: skip when the notice was already issued.
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO meta (key, value)
		 SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM meta WHERE key = ?)`,
		vcPrunePendingKey, fmt.Sprintf("%d\t%s", pruned, trigger), vcPrunedKey); err != nil {
		vcLogger(ctx).Debug("vector clock pruned; notice not recorded", "error", err, "dropped", pruned)
	}
}

// reportVCPrune issues the once-per-database prune notice after a write
// committed: if a prune was marked and no notice exists yet, it records
// vcPrunedKey with the Store's clock and logs one Warn through the Store's
// logger. Cheap when nothing is pending (one primary-key read). Failures are
// logged at debug level and never fail the committed write.
func (s *Store) reportVCPrune(ctx context.Context) {
	if s.vcReported.Load() {
		return
	}
	var pending string
	err := s.writeDB.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = ?`, vcPrunePendingKey).Scan(&pending)
	if err != nil {
		return // nothing pending (sql.ErrNoRows) or unreadable: try again next write
	}
	at := s.clock().UTC().Format(time.RFC3339)
	first, err := s.claimVCPruneNotice(ctx, at)
	if err != nil {
		s.logger.Debug("vector clock prune notice not recorded", "error", err)
		return
	}
	s.vcReported.Store(true)
	if !first {
		return
	}
	dropped, trigger, _ := strings.Cut(pending, "\t")
	n, _ := strconv.Atoi(dropped)
	s.logger.Warn("vector clock pruned: more authors observed than the sync limit; "+
		"the smallest counters of other authors were dropped (local writes are unaffected; "+
		"conflict detection is less exact for the dropped authors, convergence is not)",
		"limit", model.MaxVectorClockEntries, "dropped", n, "trigger_author", trigger, "first_at", at)
}

// claimVCPruneNotice records vcPrunedKey = at and clears the pending mark in
// one transaction. It reports whether this call recorded the key, so only
// one caller (even across processes) logs the Warn.
func (s *Store) claimVCPruneNotice(ctx context.Context, at string) (first bool, err error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin prune notice: %w", err)
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO meta (key, value) VALUES (?, ?)`, vcPrunedKey, at)
	if err != nil {
		_ = tx.Rollback() //nolint:errcheck // the exec error is the one reported
		return false, fmt.Errorf("record prune notice: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, vcPrunePendingKey); err != nil {
		_ = tx.Rollback() //nolint:errcheck // the exec error is the one reported
		return false, fmt.Errorf("clear prune mark: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit prune notice: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
