// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// Reads behind the node id grammar reports of `mtix sync status` and `mtix
// sync doctor` (MTIX-187). Since MTIX-187 a pull quarantines an event that
// names a task id outside the node id grammar and an import refuses one,
// but a store may already hold such a task from an earlier sync. Such a
// task stays readable and editable here; these reads only report it.

// invalidNodeIDReason is the text every reason of an event quarantined for
// a task id outside the node id grammar holds (validator.ErrInvalidNodeID
// followed by the event id).
const invalidNodeIDReason = "invalid node id: event "

// InvalidNodeID is a local task whose id is outside the node id grammar.
type InvalidNodeID struct {
	// ID is the task's id, as stored.
	ID string
	// UID is the task's durable uid; commands that resolve a reference
	// accept it in place of the id (`mtix show <uid>`). Empty when the
	// task has none.
	UID string
	// Deleted is true for a deleted task.
	Deleted bool
}

// InvalidNodeIDs returns the local tasks, deleted ones included, whose id is
// outside the node id grammar (model.ValidateSyncNodeID), in id order. The
// grammar is checked in Go, so every id is read; a store holds few enough
// tasks for that.
func (s *Store) InvalidNodeIDs(ctx context.Context) ([]InvalidNodeID, error) {
	// Every task's id, uid and deleted flag.
	rows, err := s.Query(ctx,
		`SELECT id, COALESCE(uid, ''), deleted_at IS NOT NULL FROM nodes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read node ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []InvalidNodeID
	for rows.Next() {
		var n InvalidNodeID
		if err := rows.Scan(&n.ID, &n.UID, &n.Deleted); err != nil {
			return nil, fmt.Errorf("read node ids: %w", err)
		}
		if model.ValidateSyncNodeID(n.ID) != nil {
			out = append(out, n)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read node ids: %w", err)
	}
	return out, nil
}

// CountQuarantinedInvalidIDs returns how many pulled events are quarantined
// because they name a task id outside the node id grammar (MTIX-187). They
// are also counted by CountQuarantined.
func (s *Store) CountQuarantinedInvalidIDs(ctx context.Context) (int, error) {
	var n int
	// Pulled events (not held push events) whose reason is the grammar's.
	if err := s.QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_quarantine WHERE source <> 'push' AND instr(reason, ?) > 0`,
		invalidNodeIDReason).Scan(&n); err != nil {
		return 0, fmt.Errorf("count quarantined invalid node ids: %w", err)
	}
	return n, nil
}
