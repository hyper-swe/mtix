// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"strconv"
)

// SetMaxQueueSize stores the cap on pending sync events that the emit path and
// an import merge enforce (sync.max_queue_size, FR-18); 0 is unlimited. The
// command layer calls it with the sync.max_queue_size config value
// (MTIX-112.17.16.16). It writes nothing when the stored cap is the same.
func (s *Store) SetMaxQueueSize(ctx context.Context, limit int64) error {
	want := strconv.FormatInt(limit, 10)
	var have string
	// The cap the emit path reads from meta.
	if err := s.readDB.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'sync.max_queue_size'`).Scan(&have); err == nil && have == want {
		return nil
	}
	if _, err := s.writeDB.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = 'sync.max_queue_size'`, want); err != nil {
		return fmt.Errorf("store sync.max_queue_size: %w", err)
	}
	return nil
}

// QueueDepth returns the number of pending sync events and the stored cap on
// them (0 is unlimited), read without writing, for the doctor
// (MTIX-112.17.16.16).
func (s *Store) QueueDepth(ctx context.Context) (pending, limit int64, err error) {
	var raw string
	// The cap the emit path enforces, and the events waiting unpushed.
	if err = s.readDB.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'sync.max_queue_size'`).Scan(&raw); err != nil {
		return 0, 0, fmt.Errorf("read sync.max_queue_size: %w", err)
	}
	if limit, err = strconv.ParseInt(raw, 10, 64); err != nil {
		return 0, 0, fmt.Errorf("parse sync.max_queue_size %q: %w", raw, err)
	}
	if err = s.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`).Scan(&pending); err != nil {
		return 0, 0, fmt.Errorf("count pending sync_events: %w", err)
	}
	return pending, limit, nil
}
