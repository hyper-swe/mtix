// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// AttachPending is an untrusted request for routing side writes. It grants
// nothing; the service checks board, path, payload and worktree membership.
type AttachPending struct {
	ID                                      int64
	Kind, BoardKey, Path, Payload, QueuedAt string
}

// EnqueueAttachPending durably appends one side write to the local queue.
func (s *Store) EnqueueAttachPending(ctx context.Context, row AttachPending) (int64, error) {
	var id int64
	err := s.WriteDB().QueryRowContext(ctx, `INSERT INTO attach_pending(kind,board_key,path,payload,queued_at) VALUES(?,?,?,?,?) RETURNING id`, row.Kind, row.BoardKey, row.Path, row.Payload, row.QueuedAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("enqueue attach side write: %w", err)
	}
	return id, nil
}

// ListAttachPending reads a stable ordered snapshot, including forged rows
// which the service must reject with an operator-local diagnostic.
func (s *Store) ListAttachPending(ctx context.Context) (out []AttachPending, err error) {
	rows, err := s.ReadDB().QueryContext(ctx, `SELECT id,kind,board_key,path,payload,queued_at FROM attach_pending ORDER BY queued_at,id`)
	if err != nil {
		return nil, fmt.Errorf("list attach side writes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row AttachPending
		if err = rows.Scan(&row.ID, &row.Kind, &row.BoardKey, &row.Path, &row.Payload, &row.QueuedAt); err != nil {
			return nil, fmt.Errorf("scan attach side write: %w", err)
		}
		out = append(out, row)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("finish reading attach side writes: %w", err)
	}
	return out, nil
}

// DeleteAttachPending removes only the exact row whose side write completed.
// A changed or replaced row stays queued even if another drain read it first.
func (s *Store) DeleteAttachPending(ctx context.Context, row AttachPending) (bool, error) {
	var id int64
	err := s.WriteDB().QueryRowContext(ctx, `DELETE FROM attach_pending WHERE id=? AND kind=? AND board_key=? AND path=? AND payload=? AND queued_at=? RETURNING id`, row.ID, row.Kind, row.BoardKey, row.Path, row.Payload, row.QueuedAt).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("clear attach side write: %w", err)
	}
	return true, nil
}
