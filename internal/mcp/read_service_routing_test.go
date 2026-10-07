// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/stretchr/testify/require"
	"log/slog"
	"testing"
	"time"
)

type mcpReadBackend struct {
	store.Store
	calls map[string]int
}

func (s *mcpReadBackend) GetNode(context.Context, string) (*model.Node, error) {
	s.calls["node"]++
	return &model.Node{ID: "TEST-1"}, nil
}
func (s *mcpReadBackend) ListNodes(context.Context, store.NodeFilter, store.ListOptions) ([]*model.Node, int, error) {
	s.calls["list"]++
	return []*model.Node{{ID: "TEST-1"}}, 1, nil
}
func (s *mcpReadBackend) GetDirectChildren(context.Context, string) ([]*model.Node, error) {
	s.calls["children"]++
	return []*model.Node{}, nil
}
func (s *mcpReadBackend) GetBlockers(context.Context, string) ([]*model.Dependency, error) {
	s.calls["blockers"]++
	return []*model.Dependency{}, nil
}

func (s *mcpReadBackend) ResolveDisplayPathByUID(context.Context, string) (string, error) {
	s.calls["uid"]++
	return "TEST-1", nil
}

func TestMCPReads_UseOwningServiceBackend(t *testing.T) {
	for _, tt := range []struct{ tool, args, operation string }{
		{"mtix_show", `{"id":"TEST-1"}`, "node"}, {"mtix_list", `{}`, "list"}, {"mtix_briefing", `{}`, "list"},
		{"mtix_show", `{"id":"0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"}`, "uid"},
		{"mtix_stats", `{}`, "list"}, {"mtix_progress", `{"id":"TEST-1"}`, "children"}, {"mtix_orphans", `{}`, "list"},
		{"mtix_search", `{"query":"x"}`, "list"}, {"mtix_blocked", `{}`, "list"}, {"mtix_dep_show", `{"id":"TEST-1"}`, "blockers"},
	} {
		t.Run(tt.tool, func(t *testing.T) {
			raw := newInboxTestStore(t)
			backend := &mcpReadBackend{Store: raw, calls: map[string]int{}}
			nodes := service.NewNodeService(backend, nil, nil, slog.Default(), time.Now)
			reg := NewToolRegistry()
			RegisterNodeTools(reg, nodes)
			RegisterWorkflowTools(reg, nodes, nil)
			RegisterDepTools(reg, service.NewDependencyServiceFromNodeService(nodes))
			RegisterAnalyticsTools(reg, nodes, nil, nil)
			_, err := reg.Call(t.Context(), tt.tool, json.RawMessage(tt.args))
			require.NoError(t, err)
			require.Greater(t, backend.calls[tt.operation], 0, "handler must use actual owning service")
		})
	}
}

func TestMCPWorkflow_UsesRealOwningSyncService(t *testing.T) {
	t.Setenv("MTIX_SYNC_DSN", "postgres://user:ROUTING_SECRET@hub.example/mtix")
	backend := newInboxTestStore(t)
	owner := service.NewSyncService(backend, slog.Default(), time.Now)
	reg := NewToolRegistry()
	RegisterSyncWorkflowTool(reg, owner, t.TempDir())
	result, err := reg.Call(t.Context(), "mtix_sync_workflow", nil)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Contains(t, result.Content[0].Text, "State: sync-configured-no-hub")
	require.NotContains(t, result.Content[0].Text, "ROUTING_SECRET")
}
