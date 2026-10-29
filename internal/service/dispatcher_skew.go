// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// DispatcherHeartbeatStore reads host dispatcher heartbeats and the board's
// dispatch protocol floor.
type DispatcherHeartbeatStore interface {
	ListDispatcherHeartbeats(ctx context.Context, hostID, uid string) ([]sqlite.DispatcherHeartbeat, error)
	MinDispatchProtocol(ctx context.Context) (int, error)
}

// DispatcherStatus is one dispatcher of this host and user, judged against
// this binary's attach protocol and the board's min_dispatch_protocol.
type DispatcherStatus struct {
	PID        int64
	Protocol   int
	Version    string
	Older      bool
	BelowFloor bool
	Floor      int
}

// NeedsAttention reports a dispatcher older than this CLI or below the floor.
func (d DispatcherStatus) NeedsAttention() bool { return d.Older || d.BelowFloor }

// DispatcherSkew finds dispatchers older than this binary (ADR-007 §6.4,
// doctor skew.dispatchers).
type DispatcherSkew struct {
	store    DispatcherHeartbeatStore
	hostID   string
	uid      string
	protocol int
}

// NewDispatcherSkew judges the dispatchers of hostID and uid against protocol.
func NewDispatcherSkew(store DispatcherHeartbeatStore, hostID, uid string, protocol int) *DispatcherSkew {
	return &DispatcherSkew{store: store, hostID: hostID, uid: uid, protocol: protocol}
}

// Statuses classifies every heartbeat of this host and user.
func (d *DispatcherSkew) Statuses(ctx context.Context) ([]DispatcherStatus, error) {
	floor, err := d.store.MinDispatchProtocol(ctx)
	if err != nil {
		return nil, fmt.Errorf("read dispatch protocol floor: %w", err)
	}
	rows, err := d.store.ListDispatcherHeartbeats(ctx, d.hostID, d.uid)
	if err != nil {
		return nil, fmt.Errorf("read dispatcher heartbeats: %w", err)
	}
	out := make([]DispatcherStatus, 0, len(rows))
	for _, hb := range rows {
		out = append(out, DispatcherStatus{
			PID: hb.PID, Protocol: hb.Protocol, Version: hb.Version, Floor: floor,
			Older: hb.Protocol < d.protocol, BelowFloor: hb.Protocol < floor,
		})
	}
	return out, nil
}
