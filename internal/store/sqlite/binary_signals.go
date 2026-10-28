// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store"
)

// HistoryReaderCapability and BinarySignal are the store-layer contract of
// the history.export guard (MTIX-95.31.19.6); the sqlite store implements it.
const HistoryReaderCapability = store.HistoryReaderCapability

// BinarySignal is store.BinarySignal.
type BinarySignal = store.BinarySignal

// BinarySignals returns the build version of every session registration and
// dispatcher heartbeat on this board, across all hosts and users: the
// history.export guard needs to know every build that touches the board, not
// only this host's. Routing metadata only; nothing is changed.
func (s *Store) BinarySignals(ctx context.Context) ([]BinarySignal, error) {
	// Every registration's version and last-seen time, then every dispatcher
	// heartbeat's version and seen time; both tables are board-local and never
	// synced, so they describe the builds running against this copy; the
	// capabilities column says whether each build holds task history.
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT 'registration', agent_id, registrar_version, capabilities, last_seen FROM agent_registrations
		UNION ALL
		SELECT 'daemon', host_id || ':' || pid, version, capabilities, seen_at FROM dispatcher_heartbeat
		ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("list binary signals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BinarySignal
	for rows.Next() {
		var b BinarySignal
		if err := rows.Scan(&b.Source, &b.Name, &b.Version, &b.Capabilities, &b.SeenAt); err != nil {
			return nil, fmt.Errorf("scan binary signal: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate binary signals: %w", err)
	}
	return out, nil
}
