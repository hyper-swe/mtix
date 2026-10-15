// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// historyRetentionField is the field of the retention_drop marker.
const historyRetentionField = "retention"

// SetHistoryLimitSource installs the reader of the history.max_entries_per_task
// limit. Nil, the default, and a result of zero or less mean unset: nothing is
// trimmed (MTIX-95.31.19.4). Call once while wiring the store.
func (s *Store) SetHistoryLimitSource(limit func() int) { s.historyLimit = limit }

func (s *Store) historyLimitValue() int {
	if s.historyLimit == nil {
		return 0
	}
	return s.historyLimit()
}

// retentionKeep returns how many change entries a task keeps when a trim run
// brings it under the limit: floor(0.9N), and never fewer than one. The gap
// between N and the floor keeps trim runs, and so markers, rare.
func retentionKeep(limit int) int {
	return max(limit*9/10, 1)
}

// trimHistory enforces history.max_entries_per_task for the tasks that just
// got entries, in the append transaction (MTIX-95.31.19.4, ADR-006 A3). A task
// whose change entries exceed the limit loses its oldest, in (ts, then append) order, down to retentionKeep, and gets one new retention_drop marker. Only
// kind change counts and is trimmed: markers, redactions and any other record
// are exempt, so a marker is never trimmed and trimming never trims itself.
// Unset means nothing is trimmed. Lowering the limit trims only at the next
// append; there is no sweep.
func (s *Store) trimHistory(ctx context.Context, tx *sql.Tx, uids map[string]bool, actor string) error {
	if len(uids) == 0 {
		return nil
	}
	limit := s.historyLimitValue()
	if limit < 1 {
		return nil
	}
	sorted := make([]string, 0, len(uids))
	for uid := range uids {
		sorted = append(sorted, uid)
	}
	sort.Strings(sorted)
	for _, uid := range sorted {
		if err := s.trimTask(ctx, tx, uid, limit, actor); err != nil {
			return err
		}
	}
	return nil
}

// trimTask trims one task when its change entries exceed limit.
func (s *Store) trimTask(ctx context.Context, tx *sql.Tx, uid string, limit int, actor string) error {
	now := s.clock().UTC()
	nowText := now.Format(historyTimeLayout)
	// Only entries dated at or before the store clock count toward the limit
	// and can be dropped: an entry inside the import's skew window (a peer clock
	// a little ahead) is neither counted nor trimmed until the clock passes it,
	// so nothing trimmed ever lies after the cutoff, a re-import cannot bring
	// it back, and a run of such entries cannot eat the task's own recent
	// changes. RFC 3339 times of this fixed width compare as text. Kind change
	// only: every other kind is exempt.
	var eligible int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_history WHERE task_uid = ? AND kind = 'change' AND ts <= ?`, uid, nowText).Scan(&eligible); err != nil {
		return fmt.Errorf("count history of %s: %w", uid, err)
	}
	if eligible <= limit {
		return nil
	}
	drop := eligible - retentionKeep(limit)
	var cutoff string
	// The newest entry to drop gives the marker its cutoff.
	if err := tx.QueryRowContext(ctx, `SELECT ts FROM task_history WHERE task_uid = ? AND kind = 'change' AND ts <= ?
		ORDER BY ts, rowid LIMIT 1 OFFSET ?`, uid, nowText, drop-1).Scan(&cutoff); err != nil {
		return fmt.Errorf("find history cutoff of %s: %w", uid, err)
	}
	// Delete the oldest change entries only; every other kind is exempt.
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_history WHERE entry_id IN (
		SELECT entry_id FROM task_history WHERE task_uid = ? AND kind = 'change' AND ts <= ?
		ORDER BY ts, rowid LIMIT ?)`, uid, nowText, drop); err != nil {
		return fmt.Errorf("trim history of %s: %w", uid, err)
	}
	count64 := strconv.Itoa(drop)
	// The cutoff is kept in a table local to this copy of the board, which no
	// export, import or sync touches: it alone drives the union's skip rule.
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_retention_cutoff (task_uid, cutoff_ts, updated_at)
		VALUES (?, ?, ?) ON CONFLICT (task_uid) DO UPDATE SET
		cutoff_ts = MAX(cutoff_ts, excluded.cutoff_ts), updated_at = excluded.updated_at`,
		uid, cutoff, now.Format(historyTimeLayout)); err != nil {
		return fmt.Errorf("record history cutoff of %s: %w", uid, err)
	}
	// The marker is plain history, for display and audit; it is never read as a cutoff.
	marker := model.HistoryEntry{TaskUID: uid, Timestamp: now, Actor: actor,
		Origin: model.HistoryOriginSystem, Kind: model.HistoryKindRetentionDrop,
		Field: historyRetentionField, Old: &cutoff, New: &count64}
	return appendHistoryEntry(ctx, tx, marker)
}

// validRetentionMarker reports whether a retention_drop entry is well formed
// enough to be stored: field retention, a cutoff in the fixed-width form that
// task_history stores, and a positive whole count. An unreadable marker is
// skipped on import and ignored as a cutoff.
func validRetentionMarker(field string, old, count *string) bool {
	if field != historyRetentionField || old == nil || count == nil {
		return false
	}
	if t, err := time.Parse(historyTimeLayout, *old); err != nil || t.Format(historyTimeLayout) != *old {
		return false
	}
	n, err := strconv.Atoi(*count)
	return err == nil && n > 0 && strconv.Itoa(n) == *count
}
