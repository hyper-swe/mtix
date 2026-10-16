// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestInvalidNodeIDs_StoreHoldsSome_ReturnsThemInIDOrder: only the tasks
// whose id is outside the node id grammar are returned, with their uid
// (MTIX-187).
func TestInvalidNodeIDs_StoreHoldsSome_ReturnsThemInIDOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"TEST-1", "TEST-2é", "DEP_ADD-1", "TEST-1é.4"} {
		parent, depth := "", 0
		if id == "TEST-1é.4" {
			parent, depth = "TEST-1", 1
		}
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, ParentID: parent, Project: "TEST", Depth: depth, Seq: i + 1, Title: "task " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h", CreatedAt: now, UpdatedAt: now,
		}))
	}

	got, err := s.InvalidNodeIDs(ctx)

	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "TEST-1é.4", got[0].ID)
	require.Equal(t, "TEST-2é", got[1].ID)
	require.NotEmpty(t, got[0].UID, "the uid lets a command address the task")
	require.False(t, got[0].Deleted)
}

// TestInvalidNodeIDs_EmptyStore_ReturnsNone: a store with no task reports
// none.
func TestInvalidNodeIDs_EmptyStore_ReturnsNone(t *testing.T) {
	got, err := newTestStore(t).InvalidNodeIDs(context.Background())

	require.NoError(t, err)
	require.Empty(t, got)
}

// TestCountQuarantinedInvalidIDs_MixedReasons_CountsOnlyGrammarRefusals:
// only pulled events quarantined for a task id outside the grammar are
// counted; other reasons and held push events are not (MTIX-187).
func TestCountQuarantinedInvalidIDs_MixedReasons_CountsOnlyGrammarRefusals(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rows := []sqlite.QuarantinedEvent{
		{EventID: "e1", Source: "pull", Reason: `envelope validation: invalid node id: event e1 node_id "TEST-1é.4": x`},
		{EventID: "e2", Source: "sweep", Reason: `envelope validation: invalid node id: event e2 parent_id "TEST-1é": x`},
		{EventID: "e3", Source: "pull", Reason: "constraint failed: FOREIGN KEY constraint failed"},
		{EventID: "e4", Source: "push", Reason: `invalid node id: event e4 node_id "x"`},
	}
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, q := range rows {
			q.RawEvent, q.FirstSeen, q.LastAttempt, q.CLIVersion = "{}", "2026-10-01T00:00:00Z", "2026-10-01T00:00:00Z", "test"
			if err := sqlite.QuarantineEvent(ctx, tx, q); err != nil {
				return err
			}
		}
		return nil
	}))

	n, err := s.CountQuarantinedInvalidIDs(ctx)

	require.NoError(t, err)
	require.Equal(t, 2, n)
}
