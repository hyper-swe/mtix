// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

type idleExportFixture struct {
	svc     *service.SyncService
	store   *sqlite.Store
	dir     string
	logger  *slog.Logger
	logs    *bytes.Buffer
	clock   func() time.Time
	advance func()
}

func newIdleExportFixture(t *testing.T, level slog.Level) idleExportFixture {
	t.Helper()
	_, store, project := newTestSyncService(t)
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store.SetClock(clock)
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: level}))
	addIdleExportNode(t, store, 1)
	return idleExportFixture{
		svc: service.NewSyncService(store, logger, clock), store: store,
		dir: filepath.Join(project, ".mtix"), logger: logger, logs: logs, clock: clock,
		advance: func() { now = now.Add(time.Minute) },
	}
}

func addIdleExportNode(t *testing.T, store *sqlite.Store, seq int) {
	t.Helper()
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	node := &model.Node{ID: fmt.Sprintf("TEST-%d", seq), Project: "TEST", Seq: seq,
		Title: fmt.Sprintf("Export node %d", seq), Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeIssue,
		CreatedAt: now, UpdatedAt: now}
	node.ContentHash = node.ComputeHash()
	require.NoError(t, store.CreateNode(context.Background(), node))
}

func idleExportBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}

func assertIdleExportMetadata(t *testing.T, f idleExportFixture, raw []byte) {
	t.Helper()
	var board sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &board))
	require.NoError(t, sqlite.ValidateExport(&board))
	expected := hashBytes(raw)
	require.Equal(t, expected, string(idleExportBytes(t, filepath.Join(f.dir, "data", "sync.sha256"))))
	var stored string
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(), "SELECT value FROM meta WHERE key = 'last_export_hash'").Scan(&stored))
	require.Equal(t, expected, stored, "own-export recognition must retain the hash of actual bytes")
	status, err := f.svc.PendingExports(context.Background(), f.dir)
	require.NoError(t, err)
	require.Zero(t, status.Count, "unchanged snapshots must retire their pending cutoff")
}
