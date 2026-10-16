// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// exportWithInvalidID returns a store holding TEST-1 and TEST-1é.4 (a task
// id outside the node id grammar, as a sync before MTIX-187 could bring
// one in) with a dependency from TEST-1 to it, and the store's export.
func exportWithInvalidID(t *testing.T) (*sqlite.Store, *sqlite.ExportData) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"TEST-1", "TEST-1é.4"} {
		parent, depth := "", 0
		if i == 1 {
			parent, depth = "TEST-1", 1
		}
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, ParentID: parent, Project: "TEST", Depth: depth, Seq: i + 1,
			Title: "imported task " + id, Status: model.StatusOpen, Priority: model.PriorityMedium,
			Weight: 1.0, NodeType: model.NodeTypeIssue, ContentHash: "h", CreatedAt: now, UpdatedAt: now,
		}))
	}
	data, err := s.Export(ctx, "TEST", "0.1.0")
	require.NoError(t, err)
	return s, data
}

// TestImport_InvalidNodeIDNotHeld_Refused: an import file that holds a task
// id outside the node id grammar is refused, and writes nothing, when the
// store does not already hold that task (MTIX-187).
func TestImport_InvalidNodeIDNotHeld_Refused(t *testing.T) {
	for _, mode := range []sqlite.ImportMode{sqlite.ImportModeReplace, sqlite.ImportModeMerge} {
		t.Run(string(mode), func(t *testing.T) {
			_, data := exportWithInvalidID(t)
			dst := newTestStore(t)
			ctx := context.Background()

			_, err := dst.Import(ctx, data, mode, false)

			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.ErrorContains(t, err, "invalid node id")
			require.ErrorContains(t, err, `"TEST-1é.4"`)
			_, err = dst.GetNode(ctx, "TEST-1")
			require.ErrorIs(t, err, model.ErrNotFound, "a refused import writes nothing")
		})
	}
}

// TestImport_InvalidNodeIDHeld_Accepted: a store that already holds such a
// task imports a file that holds it, such as its own export, so a board
// that holds one keeps working (MTIX-187).
func TestImport_InvalidNodeIDHeld_Accepted(t *testing.T) {
	for _, mode := range []sqlite.ImportMode{sqlite.ImportModeReplace, sqlite.ImportModeMerge} {
		t.Run(string(mode), func(t *testing.T) {
			src, data := exportWithInvalidID(t)
			ctx := context.Background()

			_, err := src.Import(ctx, data, mode, false)

			require.NoError(t, err)
			node, err := src.GetNode(ctx, "TEST-1é.4")
			require.NoError(t, err)
			require.Equal(t, "imported task TEST-1é.4", node.Title)
		})
	}
}
