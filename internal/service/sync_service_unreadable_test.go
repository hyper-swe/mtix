// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Regression for the MTIX-95.31.1 round-3 review finding: one node whose
// annotations cell cannot be parsed made every export fail. Auto-export only
// logged it, so local writes never reached tasks.json, and the conflict check
// read the failed export as "no conflict", so the next command replace-imported
// a pulled tasks.json and silently lost a local edit. Written red-first.
package service_test

import (
	"context"
	"encoding/json"
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

// TestAutoImport_UnreadableLocalColumn_RefusesAndKeepsLocalEdit replays the
// reviewer's scenario: SCR-2's annotations cell is corrupt, a local edit to
// SCR-1 cannot be exported, a teammate's tasks.json arrives by git pull, and
// the next command runs auto-import. The import must fail closed, naming
// mtix recover and the unreadable node and column, and change nothing: the
// local edit and the corrupt cell stay as they were. With and without a
// stored database hash (the conflict baseline).
func TestAutoImport_UnreadableLocalColumn_RefusesAndKeepsLocalEdit(t *testing.T) {
	for _, withBaseline := range []bool{true, false} {
		name := "no conflict baseline"
		if withBaseline {
			name = "with conflict baseline"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			svc, st, dir := newTestSyncService(t)
			mtixDir := filepath.Join(dir, ".mtix")
			tasksPath := filepath.Join(mtixDir, "tasks.json")
			now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
			for i, id := range []string{"SCR-1", "SCR-2"} {
				require.NoError(t, st.CreateNode(ctx, &model.Node{
					ID: id, Project: "SCR", Depth: 0, Seq: i + 1, Title: "Task " + id,
					Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
					NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: now, UpdatedAt: now,
				}))
			}
			require.NoError(t, svc.AutoExport(ctx, mtixDir))

			// A teammate's board: SCR-1 retitled upstream.
			pulled, err := st.Export(ctx, "", "")
			require.NoError(t, err)
			pulled.Nodes[0].Title = "Teammate edit"
			pulled.Nodes[0].ContentHash = "h-teammate"
			require.NoError(t, sqlite.RecomputeExportChecksum(pulled))
			pulledBytes, err := json.MarshalIndent(pulled, "", "  ")
			require.NoError(t, err)

			// SCR-2's annotations cell is corrupted; the local edit to SCR-1
			// then cannot be exported.
			_, err = st.WriteDB().ExecContext(ctx,
				`UPDATE nodes SET annotations = ? WHERE id = ?`, `[{"id": "torn"`, "SCR-2")
			require.NoError(t, err)
			localTitle := "Local edit"
			require.NoError(t, st.UpdateNode(ctx, "SCR-1", &store.NodeUpdate{Title: &localTitle}))
			exportErr := svc.AutoExport(ctx, mtixDir)
			require.Error(t, exportErr, "the export cannot succeed with an unreadable cell")
			assert.Contains(t, exportErr.Error(), "mtix recover", "the export error must name the remedy")

			if !withBaseline {
				require.NoError(t, os.Remove(filepath.Join(mtixDir, "data", "sync-db.sha256")))
			}
			require.NoError(t, os.WriteFile(tasksPath, pulledBytes, 0o644)) // git pull

			err = svc.AutoImport(ctx, mtixDir)
			require.Error(t, err, "auto-import must refuse when the local store cannot be exported")
			assert.Contains(t, err.Error(), "mtix recover")
			assert.Contains(t, err.Error(), "SCR-2")
			assert.Contains(t, err.Error(), "annotations")

			var title string
			require.NoError(t, st.QueryRow(ctx, `SELECT title FROM nodes WHERE id = ?`, "SCR-1").Scan(&title))
			assert.Equal(t, localTitle, title, "the local edit must survive the refused import")
			var cell string
			require.NoError(t, st.QueryRow(ctx, `SELECT annotations FROM nodes WHERE id = ?`, "SCR-2").Scan(&cell))
			assert.Equal(t, `[{"id": "torn"`, cell, "a refused import changes nothing")
		})
	}
}
