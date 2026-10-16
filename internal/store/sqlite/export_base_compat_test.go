// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// Pins ADR-006 A3.1 (released-reader compatibility of task history) for
// MTIX-95.31.15.1: the export of a populated store is byte for byte the one
// the base (main at 573ada7, before the base-ordered import) wrote, and it
// carries no top-level history or redaction carrier.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// exportCompatGolden is the export of exportCompatStore as the base wrote it.
const exportCompatGolden = "testdata/export-compat-base-573ada7.json"

// exportCompatStore builds a store whose export carries annotations and
// activity, with every value fixed.
func exportCompatStore(t *testing.T) []byte {
	t.Helper()
	s := newFreshTestStore(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for i, id := range []string{"PROJ-1", "PROJ-2"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "PROJ", Seq: i + 1, UID: "01995a1e-8c00-7a11-8b22-00000000000" + string(rune('1'+i)),
			Title: "Task " + id, Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1,
			NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: at, UpdatedAt: at,
		}))
	}
	require.NoError(t, s.SetAnnotations(ctx, "PROJ-1", []model.Annotation{
		{ID: "01J9COMPAT0000000000000001", Author: "reviewer", Text: "evidence", CreatedAt: at},
	}))
	_, err := s.WriteDB().ExecContext(ctx, `UPDATE nodes SET activity = ?, previous_status = 'open' WHERE id = 'PROJ-1'`,
		`[{"id":"01J9COMPAT0000000000000002","type":"comment","author":"lead","text":"seen","created_at":"2026-10-01T08:01:00Z"}]`)
	require.NoError(t, err)
	data, err := s.Export(ctx, "PROJ", "test")
	require.NoError(t, err)
	data.ExportedAt = ""
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	return raw
}

func TestExport_PopulatedStore_ByteCompatibleWithBase(t *testing.T) {
	got := exportCompatStore(t)
	if os.Getenv("MTIX_UPDATE_EXPORT_COMPAT") != "" {
		require.NoError(t, os.WriteFile(filepath.FromSlash(exportCompatGolden), append(got, '\n'), 0o600))
	}
	want, err := os.ReadFile(filepath.FromSlash(exportCompatGolden))
	require.NoError(t, err)
	require.Equal(t, string(want), string(got)+"\n", "the export must stay byte-compatible with the base (ADR-006 A3.1)")
}

func TestExport_PopulatedStore_HasNoHistoryCarrier(t *testing.T) {
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exportCompatStore(t), &top))
	for _, key := range []string{"history", "history_redactions"} {
		require.NotContains(t, top, key, "task history must not reach a board a released reader reads (ADR-006 A3.1)")
	}
}
