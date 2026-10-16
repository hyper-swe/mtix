// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// MCP reads use the cache after CLI startup; tools do not auto-import.
// Native tasks.json ownership must not prevent database reads (FR-15.8, MTIX-149).
func TestMCPRead_SyncLockHeld_ReturnsNodeData(t *testing.T) {
	st := newInboxTestStore(t)
	clock := func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) }
	svc := service.NewNodeService(st, nil, nil, slog.Default(), clock)
	node, err := svc.CreateNode(t.Context(), &service.CreateNodeRequest{
		Project: "TEST", Title: "MCP cached node", Description: "data remains readable",
	})
	require.NoError(t, err)
	reg := NewToolRegistry()
	RegisterNodeTools(reg, svc, WithPrimaryProject("TEST"))
	dir := t.TempDir()
	syncSvc := service.NewSyncService(st, slog.Default(), clock)
	require.NoError(t, syncSvc.ForceExport(t.Context(), dir))
	lock, err := pushlock.AcquireNamed(dir, "sync.lock")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })
	contender, err := pushlock.AcquireNamed(dir, "sync.lock")
	require.ErrorIs(t, err, pushlock.ErrLockHeld)
	require.Nil(t, contender)

	args, err := json.Marshal(map[string]string{"id": node.ID})
	require.NoError(t, err)
	result, err := reg.Call(t.Context(), "mtix_show", args)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	var got model.Node
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &got))
	require.Equal(t, node.ID, got.ID)
	require.Equal(t, node.Title, got.Title)
	require.Equal(t, node.Description, got.Description)
}
