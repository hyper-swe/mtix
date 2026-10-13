// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// Board flag keys of the sticky hub-connected mark (ADR-007 §3.5, A9).
const (
	hubMarkKey  = "hub_connected"
	hubFloorKey = "hub_mark_floor"
)

// hubMarkSources are the only evidence kinds a mark may record.
var hubMarkSources = map[string]bool{
	"foreign_event": true, "applied_events": true, "config": true,
	"secrets": true, "env_dsn": true,
}

// HubMark is the sticky "this board is connected to a hub or relay" flag.
type HubMark struct {
	Set    bool
	Source string
	SetAt  string
}

// HubMark reads the sticky hub-connected mark (MTIX-112.17.3, ADR-007 A9).
func (s *Store) HubMark(ctx context.Context) (HubMark, error) {
	var mark HubMark
	// Single primary-key lookup of the mark row in board_flags.
	err := s.readDB.QueryRowContext(ctx,
		`SELECT source, set_at FROM board_flags WHERE key = ?`, hubMarkKey,
	).Scan(&mark.Source, &mark.SetAt)
	if errors.Is(err, sql.ErrNoRows) {
		return HubMark{}, nil
	}
	if err != nil {
		return HubMark{}, fmt.Errorf("read hub mark: %w", err)
	}
	mark.Set = true
	return mark, nil
}

// MarkHubConnected sets the mark with the given evidence source. It reports
// whether the mark flipped from unset to set; an existing mark is never
// rewritten (sticky). Returns ErrInvalidInput for an unknown source.
func (s *Store) MarkHubConnected(ctx context.Context, source string) (bool, error) {
	if !hubMarkSources[source] {
		return false, fmt.Errorf("hub mark source %q: %w", source, model.ErrInvalidInput)
	}
	flipped := false
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		flipped, err = setHubMark(ctx, tx, source, clockOrDefault(s.clock)())
		return err
	})
	if err != nil {
		return false, fmt.Errorf("mark hub connected: %w", err)
	}
	return flipped, nil
}

// setHubMark writes the mark once inside the caller's transaction.
func setHubMark(ctx context.Context, tx *sql.Tx, source string, now time.Time) (bool, error) {
	// Insert the mark only when absent: the first evidence wins and the
	// mark is never rewritten.
	res, err := tx.ExecContext(ctx,
		`INSERT INTO board_flags(key, value, set_at, source) VALUES(?, '1', ?, ?)
		 ON CONFLICT(key) DO NOTHING`,
		hubMarkKey, now.UTC().Format(time.RFC3339), source)
	if err != nil {
		return false, fmt.Errorf("write hub mark: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hub mark rows: %w", err)
	}
	return n > 0, nil
}

// AppliedEventsAboveFloor reports whether an applied_events row exists above
// the reset floor: evidence of a hub even when a 0.5.x applier wrote it.
func (s *Store) AppliedEventsAboveFloor(ctx context.Context) (bool, error) {
	var found int
	// Any row with a rowid above the recorded reset floor (0 when never reset).
	err := s.readDB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM applied_events WHERE rowid > COALESCE(
		   (SELECT CAST(value AS INTEGER) FROM board_flags WHERE key = ?), 0))`,
		hubFloorKey).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("read applied events above floor: %w", err)
	}
	return found == 1, nil
}

// InferHubMark sets the mark from applied_events rows above the floor and
// reports whether it flipped (ADR-007 A9).
func (s *Store) InferHubMark(ctx context.Context) (bool, error) {
	has, err := s.AppliedEventsAboveFloor(ctx)
	if err != nil || !has {
		return false, err
	}
	return s.MarkHubConnected(ctx, "applied_events")
}

// ResetHubMark clears the mark and records max(applied_events.rowid) as the
// floor, in one transaction, so inference counts only later rows. It returns
// the floor. Callers enforce the TTY rule (ADR-007 §3.5).
func (s *Store) ResetHubMark(ctx context.Context) (int64, error) {
	var floor int64
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// Highest applied_events rowid; 0 for an empty table.
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(rowid), 0) FROM applied_events`).Scan(&floor); err != nil {
			return fmt.Errorf("read applied events floor: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM board_flags WHERE key = ?`, hubMarkKey); err != nil {
			return fmt.Errorf("clear hub mark: %w", err)
		}
		// Record the floor; later resets overwrite it.
		_, err := tx.ExecContext(ctx,
			`INSERT INTO board_flags(key, value, set_at, source) VALUES(?, ?, ?, 'reset')
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, set_at = excluded.set_at`,
			hubFloorKey, strconv.FormatInt(floor, 10), clockOrDefault(s.clock)().UTC().Format(time.RFC3339))
		if err != nil {
			return fmt.Errorf("record hub mark floor: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("reset hub mark: %w", err)
	}
	return floor, nil
}
