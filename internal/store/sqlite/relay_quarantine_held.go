// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RelayQuarantinedCreate reports whether relay ingest holds a skipped task
// creation from this peer for this task id, in any state (open, accepted or
// recreated), and returns the relay sequence of the newest one (MTIX-273). Ingest
// asks it before applying a record that names a task id: the peer's numbering
// collided with a local task, so the id in that record belongs to the peer's own
// task, which was skipped, and not to the local task that holds the same id.
func (s *Store) RelayQuarantinedCreate(ctx context.Context, peerID, nodeID string) (rs uint64, held bool, err error) {
	var n int64
	// Newest skipped creation of this task id from this peer, by publisher
	// epoch then relay sequence. The skipped rows are few, and the peer id is
	// the leading primary key column.
	err = s.QueryRow(ctx, `
		SELECT rs FROM relay_quarantine
		WHERE peer_id = ? AND node_id = ? AND op_type = 'create_node'
		ORDER BY pub_epoch DESC, rs DESC LIMIT 1`, peerID, nodeID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("look up skipped creation of %s from peer %s: %w", nodeID, peerID, err)
	}
	return uint64(n), true, nil //nolint:gosec // G115: relay sequences are non-negative and far below 2^63
}
