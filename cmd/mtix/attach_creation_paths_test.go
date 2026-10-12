// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Init and automatic mirror import use the same local creation identity boundary.
func TestStoreInstanceID_InitCommand_CreatesStableIdentity(t *testing.T) {
	saveAndResetApp(t)
	t.Chdir(t.TempDir())
	app.logger = slog.Default()
	require.NoError(t, runInit("TEST"))
	path := filepath.Join(".mtix", "data", "mtix.db")
	first, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	id, err := first.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.NoError(t, first.Close())
	second, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	reopened, err := second.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.Equal(t, id, reopened)
}
func TestStoreInstanceID_AutoImport_CreatesIndependentIdentity(t *testing.T) {
	saveAndResetApp(t)
	producer, err := sqlite.New(filepath.Join(t.TempDir(), "producer.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, producer.Close()) })
	node := seedAttachExport(t, producer)
	board, err := producer.Export(t.Context(), "", "")
	require.NoError(t, err)
	raw, err := json.Marshal(board)
	require.NoError(t, err)
	project := t.TempDir()
	dir := filepath.Join(project, ".mtix")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("prefix: TEST\nsync:\n  auto_sync: true\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks.json"), raw, 0600))
	t.Chdir(project)
	runPreRun(t, "list")
	require.NotNil(t, app.store)
	t.Cleanup(func() { require.NoError(t, app.store.Close()) })
	target := app.store
	id, err := target.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, id)
	other, err := producer.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, other, id)
	got, err := target.GetNode(t.Context(), node)
	require.NoError(t, err)
	require.Equal(t, "imported task", got.Title)
}

func seedAttachExport(t *testing.T, s *sqlite.Store) string {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, s.CreateNode(t.Context(), &model.Node{ID: "TEST-1", Project: "TEST", Title: "imported task", Status: model.StatusOpen, NodeType: model.NodeTypeAuto, Priority: model.PriorityMedium, Weight: 1, Creator: "test", CreatedAt: now, UpdatedAt: now, Seq: 1}))
	return "TEST-1"
}
