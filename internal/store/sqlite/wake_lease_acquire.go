// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Acquire and renewal serialize ownership and durable contention in one transaction.
package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// AcquireWakeLease acquires or renews board-local wake ownership (FR-20).
// A live contender commits dirty before returning ErrAlreadyClaimed.
func (s *Store) AcquireWakeLease(ctx context.Context, key WakeLeaseKey, holder string, ttl time.Duration) (WakeLease, error) {
	return s.acquireWakeLease(ctx, key, holder, ttl, rand.Reader)
}

func (s *Store) acquireWakeLease(ctx context.Context, key WakeLeaseKey, holder string, ttl time.Duration, reader io.Reader) (WakeLease, error) {
	if err := validateWakeLease(key, holder, ttl); err != nil {
		return WakeLease{}, err
	}
	var lease WakeLease
	var held bool
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		now := s.clock().UTC()
		row, readErr := readWakeLease(ctx, tx, key)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		lease = row.WakeLease
		switch {
		case readErr == nil && holder != "" && holder == row.Holder:
			lease.ExpiresAt = now.Add(ttl)
			return renewWakeLease(ctx, tx, key, lease.ExpiresAt)
		case readErr == nil && row.ExpiresAt.After(now):
			held = true
			return markWakeLeaseDirty(ctx, tx, key)
		default:
			var err error
			lease, err = createWakeLease(ctx, tx, key, WakeLease{AcquiredAt: now, ExpiresAt: now.Add(ttl)}, reader)
			return err
		}
	})
	if err != nil {
		return WakeLease{}, fmt.Errorf("acquire wake lease: %w", err)
	}
	// Returning this from the callback would roll back the dirty update.
	if held {
		lease.Holder, lease.HighWater = "", 0
		return lease, fmt.Errorf("wake lease held until %s: %w", lease.ExpiresAt.Format(time.RFC3339Nano), model.ErrAlreadyClaimed)
	}
	return lease, nil
}

func renewWakeLease(ctx context.Context, tx *sql.Tx, key WakeLeaseKey, expires time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE wake_leases SET expires_at=? WHERE agent_id=? AND host_id=? AND uid=?`, expires.Format(time.RFC3339Nano), key.AgentID, key.HostID, key.UID)
	if err != nil {
		return fmt.Errorf("renew wake lease: %w", err)
	}
	return nil
}

func markWakeLeaseDirty(ctx context.Context, tx *sql.Tx, key WakeLeaseKey) error {
	_, err := tx.ExecContext(ctx, `UPDATE wake_leases SET dirty=1 WHERE agent_id=? AND host_id=? AND uid=?`, key.AgentID, key.HostID, key.UID)
	if err != nil {
		return fmt.Errorf("mark wake lease dirty: %w", err)
	}
	return nil
}

func createWakeLease(ctx context.Context, tx *sql.Tx, key WakeLeaseKey, lease WakeLease, reader io.Reader) (WakeLease, error) {
	var err error
	lease.Holder, err = newWakeHolder(reader)
	if err != nil {
		return lease, err
	}
	lease.HighWater, err = journalTail(ctx, tx)
	if err != nil {
		return lease, err
	}
	lease.AgentID = key.AgentID
	_, err = tx.ExecContext(ctx, `INSERT INTO wake_leases (agent_id,host_id,uid,holder,high_water,acquired_at,expires_at,dirty)
		VALUES (?,?,?,?,?,?,?,0) ON CONFLICT(agent_id,host_id,uid) DO UPDATE SET
		holder=excluded.holder,high_water=excluded.high_water,acquired_at=excluded.acquired_at,expires_at=excluded.expires_at,dirty=0`,
		key.AgentID, key.HostID, key.UID, lease.Holder, lease.HighWater, lease.AcquiredAt.Format(time.RFC3339Nano), lease.ExpiresAt.Format(time.RFC3339Nano))
	if err != nil {
		return WakeLease{}, fmt.Errorf("create wake lease: %w", err)
	}
	return lease, nil
}
