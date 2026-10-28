// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// DispatcherHeartbeat is one host dispatcher's liveness row (ADR-007 §6.2-§6.4).
type DispatcherHeartbeat struct {
	HostID   string `json:"host_id"`
	UID      string `json:"uid"`
	PID      int64  `json:"pid"`
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
	SeenAt   string `json:"seen_at"`
}

// RecordDispatcherHeartbeat upserts the heartbeat for the dispatcher of one
// host and user; a dispatcher writes it on every pass.
func (s *Store) RecordDispatcherHeartbeat(ctx context.Context, hb DispatcherHeartbeat) error {
	if hb.HostID == "" || hb.UID == "" || hb.PID <= 0 || hb.Protocol < 0 || hb.SeenAt == "" {
		return fmt.Errorf("record dispatcher heartbeat: %w", model.ErrInvalidInput)
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		// One row per (host, uid): the latest heartbeat replaces the previous one.
		_, err := tx.ExecContext(ctx, `INSERT INTO dispatcher_heartbeat(host_id,uid,pid,protocol,version,seen_at,capabilities)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(host_id,uid) DO UPDATE SET pid=excluded.pid, protocol=excluded.protocol,
			version=excluded.version, seen_at=excluded.seen_at, capabilities=excluded.capabilities`,
			hb.HostID, hb.UID, hb.PID, hb.Protocol, hb.Version, hb.SeenAt, HistoryReaderCapability)
		if err != nil {
			return fmt.Errorf("record dispatcher heartbeat: %w", err)
		}
		return nil
	})
}

// ListDispatcherHeartbeats returns the heartbeat rows of one host and user.
func (s *Store) ListDispatcherHeartbeats(ctx context.Context, hostID, uid string) ([]DispatcherHeartbeat, error) {
	// Read the dispatchers this host and user run; other hosts' rows are not ours to judge.
	rows, err := s.readDB.QueryContext(ctx, `SELECT host_id,uid,pid,protocol,version,seen_at
		FROM dispatcher_heartbeat WHERE host_id=? AND uid=? ORDER BY pid`, hostID, uid)
	if err != nil {
		return nil, fmt.Errorf("list dispatcher heartbeats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DispatcherHeartbeat
	for rows.Next() {
		var hb DispatcherHeartbeat
		if err := rows.Scan(&hb.HostID, &hb.UID, &hb.PID, &hb.Protocol, &hb.Version, &hb.SeenAt); err != nil {
			return nil, fmt.Errorf("scan dispatcher heartbeat: %w", err)
		}
		out = append(out, hb)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dispatcher heartbeats: %w", err)
	}
	return out, nil
}
