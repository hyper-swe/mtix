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

// exportWithDependency returns the export of a store holding TEST-1 and its
// child TEST-1.1 with a real dependency between them, every id valid.
func exportWithDependency(t *testing.T) *sqlite.ExportData {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"TEST-1", "TEST-2"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "TEST", Seq: i + 1, Title: "task " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h", CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: now,
	}))
	data, err := s.Export(ctx, "TEST", "0.1.0")
	require.NoError(t, err)
	require.Len(t, data.Dependencies, 1, "the fixture holds a real dependency row")
	return data
}

// TestImport_InvalidParentOrEndpointNotHeld_Refused: when the only id outside
// the node id grammar is a node's parent id or a dependency endpoint that the
// store does not hold, the import is refused with the invalid node id error
// and writes nothing, not with a foreign key failure (MTIX-187).
func TestImport_InvalidParentOrEndpointNotHeld_Refused(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(d *sqlite.ExportData)
		want   string
	}{
		{"parent id", func(d *sqlite.ExportData) { d.Nodes[1].ParentID = "TEST-1é" }, `"TEST-1é"`},
		{"dependency from endpoint", func(d *sqlite.ExportData) { d.Dependencies[0].FromID = "TEST-1é" }, `"TEST-1é"`},
		{"dependency to endpoint", func(d *sqlite.ExportData) { d.Dependencies[0].ToID = "TEST-2é" }, `"TEST-2é"`},
	}
	for _, tt := range tests {
		for _, mode := range []sqlite.ImportMode{sqlite.ImportModeReplace, sqlite.ImportModeMerge} {
			t.Run(tt.name+"/"+string(mode), func(t *testing.T) {
				data := exportWithDependency(t)
				tt.mutate(data)
				data.Checksum = sqlite.RecomputeChecksumForTest(t, data)
				dst := newTestStore(t)
				ctx := context.Background()

				_, err := dst.Import(ctx, data, mode, false)

				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.ErrorContains(t, err, "invalid node id")
				require.ErrorContains(t, err, tt.want)
				_, err = dst.GetNode(ctx, "TEST-1")
				require.ErrorIs(t, err, model.ErrNotFound, "a refused import writes nothing")
			})
		}
	}
}
