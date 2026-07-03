// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
)

// RefreshBlocked re-derives a node's blocked status from its current blockers
// and auto-unblocks it if they are all resolved. It is a no-op when the node is
// not blocked, or when it still has an unresolved blocker (autoUnblockNode
// enforces both guards), so it is always safe to run.
//
// This is the manual recovery hatch for a node left sticky-blocked by a missed
// derived-state recompute — e.g. a blocker resolved on another client and
// synced in before the MTIX-44 fix, or an edge whose apply path did not
// recompute. `blocked` is otherwise fully system-managed (state_machine.go:
// blocked->open/in_progress are AutoOnly), so without this there is no way to
// clear a wrongly-stuck node short of canceling it.
func (s *Store) RefreshBlocked(ctx context.Context, id string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return autoUnblockNode(ctx, tx, id)
	})
}
