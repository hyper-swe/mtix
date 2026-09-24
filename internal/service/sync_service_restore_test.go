// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Regression for MTIX-107.39 (folded into MTIX-95.31.1, FR-15.2): after a
// git checkout or pull restores an earlier .mtix/tasks.json that mtix wrote,
// the next command's auto-import, and mtix import of that copy, must accept
// it. Written red-first.
package service_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestAutoImport_RestoredEarlierTasksJSON_Verifies replays the reported
// scenario: create a node and comment on it, keep a copy of tasks.json,
// write again (tasks.json is rewritten), restore the copy as git would, and
// run the next command's auto-import. The copy must verify and be imported,
// and a merge import of the copy (mtix import) must verify too. The plain
// board did not reproduce the failure; a title holding invalid UTF-8 did.
func TestAutoImport_RestoredEarlierTasksJSON_Verifies(t *testing.T) {
	tests := []struct {
		name  string
		title string
	}{
		{"plain text", "First task"},
		{"title holding invalid UTF-8", "bad\xffbyte title"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			svc, st, dir := newTestSyncService(t)
			mtixDir := filepath.Join(dir, ".mtix")
			tasksPath := filepath.Join(mtixDir, "tasks.json")
			now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

			require.NoError(t, st.CreateNode(ctx, &model.Node{
				ID: "SCR-1", Project: "SCR", Depth: 0, Seq: 1, Title: tt.title,
				Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
				NodeType: model.NodeTypeEpic, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
			}))
			first := model.Annotation{ID: "01J9RESTORE000000000000001", Author: "cli", Text: "first comment", CreatedAt: now}
			require.NoError(t, st.SetAnnotations(ctx, "SCR-1", []model.Annotation{first}))
			require.NoError(t, svc.AutoExport(ctx, mtixDir))
			earlier, err := os.ReadFile(tasksPath)
			require.NoError(t, err)

			renamed := "Renamed later"
			require.NoError(t, st.UpdateNode(ctx, "SCR-1", &store.NodeUpdate{Title: &renamed}))
			require.NoError(t, svc.AutoExport(ctx, mtixDir))
			require.NoError(t, os.WriteFile(tasksPath, earlier, 0o644)) // git restores the copy

			require.NoError(t, svc.AutoImport(ctx, mtixDir), "the restored copy must verify and import")
			node, err := st.GetNode(ctx, "SCR-1")
			require.NoError(t, err)
			assert.NotEqual(t, renamed, node.Title, "auto-import applied the restored copy")
			assert.Len(t, node.Annotations, 1, "the copy's comment came back with it")

			data, err := sqlite.DecodeExportData(bytes.NewReader(earlier))
			require.NoError(t, err)
			_, _, err = st.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
			require.NoError(t, err, "mtix import --mode merge of the restored copy must verify")
		})
	}
}
