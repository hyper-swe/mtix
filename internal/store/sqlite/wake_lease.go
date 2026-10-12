// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Wake ownership types and validation remain local to the SQLite store.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeLeaseKey identifies board-local wake ownership (FR-20).
type WakeLeaseKey struct{ AgentID, HostID, UID string }

// WakeLease describes the token and journal mark owned by one wake run.
type WakeLease struct {
	AgentID    string    `json:"agent_id"`
	Holder     string    `json:"holder"`
	HighWater  int64     `json:"high_water"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// ReleaseResult describes release or retained-owner rearm (FR-20).
type ReleaseResult struct {
	Outcome   string     `json:"outcome"`
	Pending   int        `json:"pending"`
	HighWater int64      `json:"high_water"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type wakeLeaseRow struct {
	WakeLease
	dirty bool
}

func validateWakeLease(key WakeLeaseKey, holder string, ttl time.Duration) error {
	if key.AgentID == "" || key.HostID == "" || key.UID == "" {
		return fmt.Errorf("wake lease identity required: %w", model.ErrInvalidInput)
	}
	if ttl < time.Minute || ttl > 24*time.Hour {
		return fmt.Errorf("wake lease ttl must be between 1m and 24h: %w", model.ErrInvalidInput)
	}
	if holder != "" {
		if len(holder) != 32 {
			return fmt.Errorf("wake lease holder must be 32 lowercase hex characters: %w", model.ErrInvalidInput)
		}
		for _, c := range holder {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return fmt.Errorf("wake lease holder must be 32 lowercase hex characters: %w", model.ErrInvalidInput)
			}
		}
	}
	return nil
}

func readWakeLease(ctx context.Context, tx *sql.Tx, key WakeLeaseKey) (wakeLeaseRow, error) {
	var row wakeLeaseRow
	var acquired, expires string
	err := tx.QueryRowContext(ctx, `SELECT holder,high_water,acquired_at,expires_at,dirty
		FROM wake_leases WHERE agent_id=? AND host_id=? AND uid=?`, key.AgentID, key.HostID, key.UID).
		Scan(&row.Holder, &row.HighWater, &acquired, &expires, &row.dirty)
	if err != nil {
		return row, fmt.Errorf("read wake lease: %w", err)
	}
	row.AgentID = key.AgentID
	row.AcquiredAt, err = time.Parse(time.RFC3339Nano, acquired)
	if err != nil {
		return row, fmt.Errorf("read wake lease acquired_at: %w", err)
	}
	row.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return row, fmt.Errorf("read wake lease expires_at: %w", err)
	}
	return row, nil
}

func newWakeHolder(reader io.Reader) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(reader, raw[:]); err != nil {
		return "", fmt.Errorf("generate wake lease holder: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
