// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// This is a compatibility witness against the checksum-verified released CLI,
// not red-first evidence for the idle-export optimization.
func TestAutoExport_054ReaderImportsIdleAndChangedNodes(t *testing.T) {
	reader := compat054Binary(t)
	ctx := t.Context()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sourceDir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(sourceDir, "data"), 0700))
	source, err := sqlite.New(filepath.Join(sourceDir, "data"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, source.Close()) })
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	source.SetClock(func() time.Time { return now })
	sync := service.NewSyncService(source, logger, func() time.Time { return now })
	node := &model.Node{
		ID: "TEST-1", Project: "TEST", Seq: 1, Title: "Compatibility witness",
		Description: "Exported by the current SyncService", Prompt: "Keep the reader compatible",
		Acceptance: "The released CLI imports all content", Status: model.StatusOpen,
		Priority: model.PriorityMedium, NodeType: model.NodeTypeEpic, Weight: 1,
		Labels: []string{"compatibility"}, ContentHash: "initial-content-hash",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	node.ContentHash = node.ComputeHash()
	require.NoError(t, source.CreateNode(ctx, node))

	project := t.TempDir()
	readerDir := filepath.Join(project, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(readerDir, "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(readerDir, "config.yaml"),
		[]byte("prefix: TEST\nsync:\n  auto_sync: false\n"), 0600))
	incoming := filepath.Join(project, "incoming.json")
	var firstNodes []json.RawMessage
	var firstChecksum string
	for i := range 3 {
		now = now.Add(time.Hour)
		require.NoError(t, sync.AutoExport(ctx, sourceDir))
		raw, nodes, checksum := autoExport054Snapshot(t, sourceDir)
		if i == 0 {
			firstNodes, firstChecksum = nodes, checksum
		} else {
			require.Equal(t, firstNodes, nodes, "idle export preserves node content")
			require.Equal(t, firstChecksum, checksum, "idle export preserves node checksum")
		}
		require.NoError(t, os.WriteFile(incoming, raw, 0600))
		attachReaderCommand(t, reader, project, "", "import", incoming)
		attachReaderCommand(t, reader, project, "", "show", node.ID, "--json")
		expected, err := source.GetNode(ctx, node.ID)
		require.NoError(t, err)
		autoExport054AssertStoredNode(t, readerDir, expected)
	}

	title := "Compatibility witness changed"
	changed, err := source.GetNode(ctx, node.ID)
	require.NoError(t, err)
	changed.Title = title
	hash := changed.ComputeHash()
	require.NoError(t, source.UpdateNode(ctx, node.ID, &store.NodeUpdate{Title: &title, ContentHash: &hash}))
	now = now.Add(time.Hour)
	require.NoError(t, sync.AutoExport(ctx, sourceDir))
	raw, nodes, checksum := autoExport054Snapshot(t, sourceDir)
	require.NotEqual(t, firstNodes, nodes, "a real change reaches the exported nodes")
	require.NotEqual(t, firstChecksum, checksum, "a real change updates the checksum")
	require.NoError(t, os.WriteFile(incoming, raw, 0600))
	attachReaderCommand(t, reader, project, "", "import", incoming)
	attachReaderCommand(t, reader, project, "", "show", node.ID, "--json")
	expected, err := source.GetNode(ctx, node.ID)
	require.NoError(t, err)
	autoExport054AssertStoredNode(t, readerDir, expected)
}

func autoExport054Snapshot(t *testing.T, mtixDir string) ([]byte, []json.RawMessage, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	var envelope struct {
		Nodes    []json.RawMessage `json:"nodes"`
		Checksum string            `json:"checksum"`
	}
	require.NoError(t, json.Unmarshal(raw, &envelope))
	require.Len(t, envelope.Nodes, 1)
	require.NotEmpty(t, envelope.Checksum)
	return raw, envelope.Nodes, envelope.Checksum
}

// Inspect the old reader's persisted cache without running current migrations.
func autoExport054AssertStoredNode(t *testing.T, mtixDir string, expected *model.Node) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(mtixDir, "data", "mtix.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM nodes").Scan(&count))
	require.Equal(t, 1, count, "sequential imports reuse the old reader's cache")
	var title, description, prompt, acceptance, status, contentHash, labels, updatedAt string
	var priority int
	require.NoError(t, db.QueryRowContext(t.Context(),
		"SELECT title,description,prompt,acceptance,status,priority,content_hash,labels,updated_at FROM nodes WHERE id=?",
		expected.ID).Scan(&title, &description, &prompt, &acceptance, &status, &priority, &contentHash, &labels, &updatedAt))
	require.Equal(t, expected.Title, title)
	require.Equal(t, expected.Description, description)
	require.Equal(t, expected.Prompt, prompt)
	require.Equal(t, expected.Acceptance, acceptance)
	require.Equal(t, string(expected.Status), status)
	require.Equal(t, int(expected.Priority), priority)
	require.Equal(t, expected.ContentHash, contentHash)
	var importedLabels []string
	require.NoError(t, json.Unmarshal([]byte(labels), &importedLabels))
	require.Equal(t, expected.Labels, importedLabels)
	require.Equal(t, expected.UpdatedAt.UTC().Format(time.RFC3339), updatedAt)
}
