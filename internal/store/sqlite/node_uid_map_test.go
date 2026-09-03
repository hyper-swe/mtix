// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestNodeUIDsByProject_GroupsByPrefixAndKeepsSoftDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mk := func(id, project string) {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: project, Title: id, Status: model.StatusOpen,
			NodeType: model.NodeTypeAuto, Priority: model.PriorityMedium, Weight: 1.0,
			Creator: "test", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Seq: 1,
		}))
	}
	mk("TEST-1", "TEST")
	mk("TEST-2", "TEST")
	mk("OTHER-1", "OTHER")

	// A soft-deleted node's create is on the hub like any other: keep it.
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET deleted_at = '2026-09-01T00:00:00Z' WHERE id = 'TEST-2'`)
	require.NoError(t, err)
	// A node with no uid cannot repair anything: omit it.
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = '' WHERE id = 'OTHER-1'`)
	require.NoError(t, err)

	got, err := s.NodeUIDsByProject(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1, "OTHER has no node with a uid")
	require.Len(t, got["TEST"], 2)
	require.NotEmpty(t, got["TEST"]["TEST-1"])
	require.NotEmpty(t, got["TEST"]["TEST-2"], "soft-deleted node keeps its uid in the map")
	require.NotEqual(t, got["TEST"]["TEST-1"], got["TEST"]["TEST-2"])
}
