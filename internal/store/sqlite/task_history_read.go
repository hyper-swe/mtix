// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// ListTaskHistory returns the history entries of the task with the given id,
// newest first, at most limit of them (MTIX-95.31.19.4). Returns ErrNotFound
// for an unknown id. Free-text fields come back value-free: the store never
// recorded their text.
func (s *Store) ListTaskHistory(ctx context.Context, id string, limit int) ([]model.HistoryEntry, error) {
	uid, err := s.historyKeyOf(ctx, id)
	if err != nil {
		return nil, err
	}
	// The task's entries, newest first, by the order they sort in everywhere.
	rows, err := s.readDB.QueryContext(ctx, `SELECT entry_id, task_uid, ts, actor, origin, kind, field,
		old_value, new_value, event_id FROM task_history WHERE task_uid = ?
		ORDER BY ts DESC, rowid DESC LIMIT ?`, uid, limit)
	if err != nil {
		return nil, fmt.Errorf("list history of %s: %w", id, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close history rows", "error", closeErr)
		}
	}()
	var out []model.HistoryEntry
	for rows.Next() {
		var e model.HistoryEntry
		var ts string
		var oldV, newV, ev sql.NullString
		var origin, kind string
		if scanErr := rows.Scan(&e.ID, &e.TaskUID, &ts, &e.Actor, &origin, &kind, &e.Field, &oldV, &newV, &ev); scanErr != nil {
			return nil, fmt.Errorf("scan history of %s: %w", id, scanErr)
		}
		e.Origin, e.Kind, e.EventID = model.HistoryOrigin(origin), model.HistoryKind(kind), ev.String
		if e.Timestamp, err = time.Parse(historyTimeLayout, ts); err != nil {
			return nil, fmt.Errorf("history entry %s has ts %q: %w", e.ID, ts, err)
		}
		if oldV.Valid {
			e.Old = &oldV.String
		}
		if newV.Valid {
			e.New = &newV.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// historyKeyOf returns the key task_history uses for the node with this id:
// its uid, or the id for a task without one. ErrNotFound if there is no node.
func (s *Store) historyKeyOf(ctx context.Context, id string) (string, error) {
	var uid sql.NullString
	// Resolve the node's history key (uid, or id when it has none).
	err := s.readDB.QueryRowContext(ctx, `SELECT uid FROM nodes WHERE id = ?`, id).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("task %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("find task %s: %w", id, err)
	}
	if uid.String == "" {
		return id, nil
	}
	return uid.String, nil
}

// HistorySizeTask is one task in a HistorySizeReport.
type HistorySizeTask struct {
	ID      string `json:"id"`
	Entries int    `json:"entries"`
}

// HistorySizeReport is the size of the task history in the store.
type HistorySizeReport struct {
	Rows     int               `json:"rows"`
	Tasks    int               `json:"tasks"`
	Largest  []HistorySizeTask `json:"largest_tasks"`
	Oversize int               `json:"oversize_tasks"`
	// Ahead counts change entries dated after the store clock (a peer clock
	// ahead of this one). They are neither counted toward the limit nor trimmed
	// until the clock passes them.
	Ahead int `json:"ahead_of_clock"`
	// CutoffsAhead counts retention cutoffs later than the store clock (recorded
	// while the clock ran ahead). The union treats them as the clock.
	CutoffsAhead int `json:"cutoffs_ahead_of_clock"`
}

// The history size statements. Each is served by an index: the grouping runs
// over idx_task_history_uid_ts and takes the top rows before any lookup, and a
// task's id is then found by two point lookups (its uid index, then the
// primary key for a task keyed by its id), never by a join on an expression
// (MTIX-95.31.19.4).
const (
	// The tasks with the most history rows, largest first.
	historyLargestSQL = `SELECT task_uid, COUNT(*) AS c FROM task_history GROUP BY task_uid ORDER BY c DESC, task_uid LIMIT ?`
	// The node id of a task keyed by its uid.
	historyIDByUIDSQL = `SELECT id FROM nodes WHERE uid = ? AND uid <> ''`
	// The node id of a task keyed by its id (a task without a uid).
	historyIDByIDSQL = `SELECT id FROM nodes WHERE id = ?`
	// Tasks holding more change entries than the limit, counting only entries
	// dated at or before the clock, as the trim does.
	historyOversizeSQL = `SELECT COUNT(*) FROM (SELECT task_uid FROM task_history
		WHERE kind = 'change' AND ts <= ? GROUP BY task_uid HAVING COUNT(*) > ?)`
	// Retention cutoffs later than the clock.
	historyCutoffsAheadSQL = `SELECT COUNT(*) FROM history_retention_cutoff WHERE cutoff_ts > ?`
	// Change entries dated after the clock.
	historyAheadSQL = `SELECT COUNT(*) FROM task_history WHERE kind = 'change' AND ts > ?`
)

// HistorySize reports the history rows, the number of tasks with history, the
// largest tasks by row count (at most top, shown by node id, or by uid for a
// task no longer on the board) and how many tasks hold more change entries
// than limit (0 = no limit, so none) (MTIX-95.31.19.4).
func (s *Store) HistorySize(ctx context.Context, top, limit int) (HistorySizeReport, error) {
	var rep HistorySizeReport
	// Total rows and tasks with history.
	if err := s.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT task_uid) FROM task_history`).Scan(&rep.Rows, &rep.Tasks); err != nil {
		return rep, fmt.Errorf("count history: %w", err)
	}
	largest, err := s.largestHistories(ctx, top)
	if err != nil {
		return rep, err
	}
	rep.Largest = largest
	nowText := s.clock().UTC().Format(historyTimeLayout)
	if err := s.readDB.QueryRowContext(ctx, historyAheadSQL, nowText).Scan(&rep.Ahead); err != nil {
		return rep, fmt.Errorf("count history dated ahead of the clock: %w", err)
	}
	if err := s.readDB.QueryRowContext(ctx, historyCutoffsAheadSQL, nowText).Scan(&rep.CutoffsAhead); err != nil {
		return rep, fmt.Errorf("count history cutoffs ahead of the clock: %w", err)
	}
	if limit < 1 {
		return rep, nil
	}
	if err := s.readDB.QueryRowContext(ctx, historyOversizeSQL, nowText, limit).Scan(&rep.Oversize); err != nil {
		return rep, fmt.Errorf("count oversize histories: %w", err)
	}
	return rep, nil
}

// largestHistories returns the top tasks by history rows, each named by node id.
func (s *Store) largestHistories(ctx context.Context, top int) ([]HistorySizeTask, error) {
	out, err := s.readLargest(ctx, top)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].ID, err = s.nodeIDOfHistoryKey(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readLargest reads the largest history keys with their row counts.
func (s *Store) readLargest(ctx context.Context, top int) (out []HistorySizeTask, err error) {
	rows, err := s.readDB.QueryContext(ctx, historyLargestSQL, top)
	if err != nil {
		return nil, fmt.Errorf("find largest histories: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close largest histories: %w", closeErr)
		}
	}()
	out = []HistorySizeTask{}
	for rows.Next() {
		var t HistorySizeTask
		if scanErr := rows.Scan(&t.ID, &t.Entries); scanErr != nil {
			return nil, fmt.Errorf("scan largest histories: %w", scanErr)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read largest histories: %w", err)
	}
	return out, nil
}

// nodeIDOfHistoryKey names a history key by its node id: found by uid, then by
// id. A key of no node on the board is returned as it is; any other lookup
// error is returned.
func (s *Store) nodeIDOfHistoryKey(ctx context.Context, key string) (string, error) {
	for _, q := range []string{historyIDByUIDSQL, historyIDByIDSQL} {
		var id string
		err := s.readDB.QueryRowContext(ctx, q, key).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("find the task of history key %s: %w", key, err)
		}
	}
	return key, nil
}
