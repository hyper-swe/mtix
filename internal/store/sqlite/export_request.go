// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

const exportRequestKey = "meta.export.pending"

// ReadExportRequest reads local intent without changing state (MTIX-130.11).
func (s *Store) ReadExportRequest(ctx context.Context) (ExportRequestState, error) {
	var raw string
	err := s.QueryRow(ctx, "SELECT value FROM meta WHERE key = ?", exportRequestKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ExportRequestState{Version: 1}, nil
	}
	if err != nil {
		return ExportRequestState{}, fmt.Errorf("read export request: %w", err)
	}
	return decodeExportRequest(raw)
}

// MarkExportRequest durably advances one coalesced generation with the normal
// FULL-WAL write guards, without mutation callbacks (FR-15.3, NFR-2.8).
func (s *Store) MarkExportRequest(ctx context.Context, requested time.Time) error {
	return s.updateExportRequest(ctx, func(state *ExportRequestState) error {
		if requested.IsZero() {
			return fmt.Errorf("zero export request time: %w", model.ErrInvalidInput)
		}
		if state.Requested == math.MaxInt64 {
			return fmt.Errorf("export request generation exhausted: %w", model.ErrConflict)
		}
		state.Requested++
		var err error
		state.Oldest, err = earlierExportTime(state.Oldest, requested)
		if err != nil {
			return err
		}
		if state.Active != nil {
			state.Late, err = earlierExportTime(state.Late, requested)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// SnapshotExportRequest commits the cutoff BEFORE reading the board snapshot.
// A restarted exporter can replace an unfinished cutoff without losing intent.
func (s *Store) SnapshotExportRequest(ctx context.Context) (int64, error) {
	var cutoff int64
	err := s.updateExportRequest(ctx, func(state *ExportRequestState) error {
		cutoff = state.Requested
		if state.Requested > state.Covered {
			state.Active, state.Late = &cutoff, ""
		}
		return nil
	})
	return cutoff, err
}

// RetireExportRequest covers only a registered successful publication cutoff.
// Newer marks retain their exact oldest age and require a subsequent export.
func (s *Store) RetireExportRequest(ctx context.Context, cutoff int64) error {
	return s.updateExportRequest(ctx, func(state *ExportRequestState) error {
		if cutoff == state.Covered && state.Requested == state.Covered {
			return nil
		}
		if state.Active == nil || *state.Active != cutoff {
			return fmt.Errorf("export request cutoff changed: %w", model.ErrConflict)
		}
		state.Covered = cutoff
		if state.Requested > cutoff {
			state.Oldest = state.Late
		} else {
			state.Oldest = ""
		}
		state.Active, state.Late = nil, ""
		return nil
	})
}

func (s *Store) updateExportRequest(ctx context.Context, update func(*ExportRequestState) error) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		state, err := readExportRequestTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := update(&state); err != nil {
			return err
		}
		if err := validateExportRequest(state); err != nil {
			return err
		}
		body, err := json.Marshal(state)
		if err != nil {
			return fmt.Errorf("encode export request: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", exportRequestKey, string(body)); err != nil {
			return fmt.Errorf("write export request: %w", err)
		}
		return nil
	}, false)
}

func readExportRequestTx(ctx context.Context, tx *sql.Tx) (ExportRequestState, error) {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", exportRequestKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ExportRequestState{Version: 1}, nil
	}
	if err != nil {
		return ExportRequestState{}, fmt.Errorf("read export request transaction: %w", err)
	}
	return decodeExportRequest(raw)
}
