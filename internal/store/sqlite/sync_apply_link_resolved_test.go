// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestApplyLinkDep_ResolvedBlocker_DoesNotBlockDependent: a synced blocks
// edge over a done, cancelled or invalidated blocker leaves the dependent
// as it is (MTIX-112.17.16.40), the same rule autoUnblockNode applies.
func TestApplyLinkDep_ResolvedBlocker_DoesNotBlockDependent(t *testing.T) {
	for _, resolved := range []model.Status{model.StatusDone, model.StatusCancelled, model.StatusInvalidated} {
		t.Run(string(resolved), func(t *testing.T) {
			s, err := New(t.TempDir(), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { _ = s.Close() })
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Second)
			require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-1", "PROJ", "Blocker", now)))
			require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-2", "PROJ", "Dependent", now)))
			markSynced(ctx, t, s)
			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, `UPDATE nodes SET status = ? WHERE id = ?`, string(resolved), "PROJ-1")
				return e
			}))

			applyOne(ctx, t, s, &model.SyncEvent{
				EventID: "evt-link", NodeID: "PROJ-1", OpType: model.OpLinkDep, AuthorID: "remote",
				Payload: mustEncode(t, &model.LinkDepPayload{
					DependsOnNodeID: "PROJ-2", DepType: string(model.DepTypeBlocks),
				}),
			})

			dep, err := s.GetNode(ctx, "PROJ-2")
			require.NoError(t, err)
			require.Equal(t, model.StatusOpen, dep.Status)
		})
	}
}
