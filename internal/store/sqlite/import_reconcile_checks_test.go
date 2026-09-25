// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestPrepareWrite_ImportChangingOneRowOutsideTheSearchIndex_RunsBeforeWriteOnce
// verifies a merge whose only change is one row that no search-index
// trigger follows (a new acceptance, or one new dependency) runs the step
// before the write once (MTIX-95.31.4.2, carried by MTIX-95.31.6): the dry
// run counts exactly one change, the smallest count that is a write, so a
// check that takes one change for none skips the backup.
func TestPrepareWrite_ImportChangingOneRowOutsideTheSearchIndex_RunsBeforeWriteOnce(t *testing.T) {
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		edit  func(t *testing.T, d *ExportData)
		check func(t *testing.T, s *Store)
	}{
		{"only the acceptance changes", func(t *testing.T, d *ExportData) {
			d.Nodes[0].Acceptance, d.Nodes[0].ContentHash = "Done when the report lists it", "h-accepted"
		}, func(t *testing.T, s *Store) {
			node, err := s.GetNode(context.Background(), "REC-1")
			require.NoError(t, err)
			assert.Equal(t, "Done when the report lists it", node.Acceptance)
		}},
		{"only one dependency is added", func(t *testing.T, d *ExportData) {
			d.Dependencies = append(d.Dependencies, exportDep{FromID: "REC-1", ToID: "REC-2",
				DepType: string(model.DepTypeBlocks), CreatedAt: created.Format(time.RFC3339)})
		}, func(t *testing.T, s *Store) {
			blockers, err := s.GetBlockers(context.Background(), "REC-2")
			require.NoError(t, err)
			assert.Len(t, blockers, 1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newInternalTestStore(t)
			for seq, id := range []string{"REC-1", "REC-2"} {
				require.NoError(t, s.CreateNode(ctx, &model.Node{
					ID: id, Project: "REC", Depth: 0, Seq: seq + 1, Title: "Task " + id,
					Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
					NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
				}))
			}
			file := func() *ExportData {
				data, err := s.Export(ctx, "", "")
				require.NoError(t, err)
				tt.edit(t, data)
				require.NoError(t, RecomputeExportChecksum(data))
				return data
			}

			changes, _, err := s.countImportChanges(ctx, file(), ImportModeMerge, nil)
			require.NoError(t, err)
			require.Equal(t, int64(1), changes, "the case changes exactly one row")

			calls := 0
			_, _, err = s.ImportReconcile(ctx, file(), ImportReconcileOptions{
				Mode: ImportModeMerge, BeforeWrite: func() error { calls++; return nil },
			})
			require.NoError(t, err)
			assert.Equal(t, 1, calls, "the step before the write runs once")
			tt.check(t, s)
		})
	}
}
