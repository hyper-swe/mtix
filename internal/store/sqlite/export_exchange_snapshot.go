// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Exchange boundaries and board rows are observed in one read-only WAL snapshot.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
)

type ExchangeBoundary struct {
	// PendingOutbound is nil for older receipts whose outbound state is unknown.
	PendingOutbound     *int64 `json:"pending_outbound,omitempty"`
	LocalSequence       int64  `json:"local_sequence"`
	PullClock           int64  `json:"pull_clock"`
	PullEventID         string `json:"pull_event_id"`
	LastSweepAt         string `json:"last_sweep_at"`
	SweepAfterID        string `json:"sweep_after_id"`
	SweepAfterCreatedAt string `json:"sweep_after_created_at"`
	SweepStartedAt      string `json:"sweep_started_at"`
	Pending             int64  `json:"pending"`
	Unresolved          int64  `json:"unresolved"`
}

func (s *Store) ExportExchangeSnapshot(ctx context.Context, project, version string, encountered []string, capBytes int64) (*ExportData, ExchangeBoundary, error) {
	var boundary ExchangeBoundary
	if err := validateExchangeIDs(encountered, capBytes); err != nil {
		return nil, boundary, err
	}
	data, err := s.readExportSnapshotWith(ctx, func(tx *sql.Tx) error {
		var err error
		boundary, err = readExchangeBoundary(ctx, tx)
		if err != nil {
			return err
		}
		boundary.Unresolved, err = countUnresolvedExchange(ctx, tx, encountered)
		return err
	})
	if err != nil {
		return nil, boundary, err
	}
	data, err = s.finishExport(data, project, version)
	return data, boundary, err
}

func validateExchangeIDs(ids []string, capBytes int64) error {
	if capBytes <= 0 {
		return fmt.Errorf("exchange snapshot: invalid cap")
	}
	remaining := capBytes
	for _, id := range ids {
		cost := int64(len(id)) + 64
		if id == "" || len(id) > 128 || cost > remaining {
			return fmt.Errorf("exchange snapshot: id budget")
		}
		remaining -= cost
	}
	return nil
}

func readExchangeBoundary(ctx context.Context, tx *sql.Tx) (ExchangeBoundary, error) {
	var b ExchangeBoundary
	var outbound int64
	// Count every pending journal row, including held push events, in this WAL read.
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0), COUNT(CASE WHEN sync_status='pending' THEN 1 END) FROM sync_events`).Scan(&b.LocalSequence, &outbound); err != nil {
		return b, fmt.Errorf("exchange snapshot: read outbound aggregate: %w", err)
	}
	b.PendingOutbound = &outbound
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_sweep_pending`).Scan(&b.Pending); err != nil {
		return b, err
	}
	var clock string
	for _, kv := range []struct {
		key string
		dst *string
	}{
		{"meta.sync.last_pulled_clock", &clock}, {"meta.sync.last_pulled_event_id", &b.PullEventID},
		{"meta.sync.last_sweep_at", &b.LastSweepAt}, {"meta.sync.sweep_after_id", &b.SweepAfterID},
		{"meta.sync.sweep_after_created_at", &b.SweepAfterCreatedAt}, {"meta.sync.sweep_started_at", &b.SweepStartedAt},
	} {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM meta WHERE key=?),'')`, kv.key).Scan(kv.dst); err != nil {
			return b, err
		}
	}
	value, err := strconv.ParseInt(clock, 10, 64)
	if err != nil || value < 0 {
		return b, fmt.Errorf("exchange snapshot: invalid pull cursor")
	}
	b.PullClock = value
	return b, nil
}

func countUnresolvedExchange(ctx context.Context, tx *sql.Tx, ids []string) (int64, error) {
	var total int64
	for start := 0; start < len(ids); start += 500 {
		raw, err := json.Marshal(ids[start:min(start+500, len(ids))])
		if err != nil {
			return 0, err
		}
		var count int64
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM json_each(?) j
   WHERE EXISTS (SELECT 1 FROM sync_quarantine q WHERE q.event_id=j.value)
    OR (NOT EXISTS (SELECT 1 FROM applied_events a WHERE a.event_id=j.value)
     AND NOT EXISTS (SELECT 1 FROM sync_events s WHERE s.event_id=j.value))`, string(raw)).Scan(&count)
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}
