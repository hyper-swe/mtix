// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
)

// PullMove is one local task a pull moved to another number: a hub create took
// the number it held (MTIX-112.17.16.39). The task keeps its uid; agents that
// hold OldID must re-resolve it.
type PullMove struct {
	OldID string `json:"old_id"`
	NewID string `json:"new_id"`
	Title string `json:"title"`
	// UID is the task's internal id, empty for a task that has none.
	UID string `json:"uid"`
}

// PullMoveMark returns the position of the end of the task history: the mark
// to pass to PullMovesSince after a pull. It is the highest history rowid, 0
// for an empty history.
func (s *Store) PullMoveMark(ctx context.Context) (int64, error) {
	var mark int64
	// The newest history row; rowids only grow while rows are appended.
	if err := s.readDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid), 0) FROM task_history`).Scan(&mark); err != nil {
		return 0, fmt.Errorf("read the task history mark: %w", err)
	}
	return mark, nil
}

// PullMovesSince returns the tasks moved by replayed hub events since mark,
// oldest first: every display_path change of origin hub recorded after it,
// with the task's title now. The moves are read from the task history, so
// they are exactly the moves that were committed.
func (s *Store) PullMovesSince(ctx context.Context, mark int64) (moves []PullMove, err error) {
	// Hub-origin display_path changes are the renumbers a replay made; the node
	// is found by uid, or by id for a task without one (history keys on both).
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT h.old_value, h.new_value, COALESCE(n.title, ''), COALESCE(n.uid, '')
		  FROM task_history h
		  LEFT JOIN nodes n ON (n.uid <> '' AND n.uid = h.task_uid) OR ((n.uid IS NULL OR n.uid = '') AND n.id = h.task_uid)
		 WHERE h.rowid > ? AND h.origin = 'hub' AND h.kind = 'change' AND h.field = 'display_path'
		 ORDER BY h.rowid`, mark)
	if err != nil {
		return nil, fmt.Errorf("read the moves of a pull: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close the moves of a pull: %w", closeErr)
		}
	}()
	for rows.Next() {
		var oldV, newV, title, uid *string
		if err = rows.Scan(&oldV, &newV, &title, &uid); err != nil {
			return nil, fmt.Errorf("scan a move of a pull: %w", err)
		}
		m := PullMove{Title: derefString(title), UID: derefString(uid)}
		if json.Unmarshal([]byte(derefString(oldV)), &m.OldID) != nil || json.Unmarshal([]byte(derefString(newV)), &m.NewID) != nil {
			continue // an entry that is not a number change (null, malformed): not a move
		}
		moves = append(moves, m)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate the moves of a pull: %w", err)
	}
	return moves, nil
}

// derefString returns *s, or "" for nil.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
