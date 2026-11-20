// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestMoveUnloggedHolder_MoveFails_LeavesNoRenumberInHistoryTracker: the move
// of an unlogged holder is noted for the transaction's history tracker only
// once its writes have succeeded. A move that fails halfway (here: the rewrite
// of the node numbers is refused) and whose event is rolled back by the
// caller's savepoint must not leave a renumber that never happened, which
// would misattribute history when the transaction commits.
func TestMoveUnloggedHolder_MoveFails_LeavesNoRenumberInHistoryTracker(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	local := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "local"})
	require.NoError(t, applyOnce(t, s, local))
	_, err := raw.Exec(`DELETE FROM sync_events WHERE op_type = 'create_node'`)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TRIGGER refuse_renumber BEFORE UPDATE OF id ON nodes
		BEGIN SELECT RAISE(ABORT, 'renumber refused'); END`)
	require.NoError(t, err)

	incoming := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "bob", 2, &model.CreateNodePayload{Title: "hub"})
	incoming.UID = incoming.EventID
	var moves int
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return s.inTxHistory(ctx, tx, historyScope{id: "MTIX-1", origin: model.HistoryOriginHub}, func() error {
			moved, moveErr := moveUnloggedHolder(ctx, tx, s, incoming, incoming.UID)
			require.ErrorContains(t, moveErr, "renumber refused")
			require.False(t, moved)
			v, ok := s.history.Load(tx)
			require.True(t, ok)
			moves = len(v.(*historyTracker).moves) //nolint:errcheck,forcetypeassert // the test stored it through inTxHistory
			return nil
		})
	}))
	require.Zero(t, moves, "a failed move is not noted as a renumber")
}
