// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// RegisterAnalyticsTools registers analytics and query MCP tools per MTIX-6.2.6.
func RegisterAnalyticsTools(
	reg *ToolRegistry,
	st store.Store,
	agentSvc *service.AgentService,
	configSvc *service.ConfigService,
) {
	registerStatsTool(reg, st)
	registerProgressTool(reg, st)
	registerStaleTool(reg, agentSvc, configSvc)
	registerOrphansTool(reg, st)
}

func registerStatsTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_stats",
		Description: "Get project statistics (node counts by status)",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"under": {Type: "string", Description: "Subtree root ID (optional, for scoped stats)"},
			},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct{ Under string `json:"under"` }
		if args != nil {
			_ = json.Unmarshal(args, &p)
		}

		// Count nodes per status.
		statuses := []model.Status{
			model.StatusOpen, model.StatusInProgress, model.StatusDone,
			model.StatusBlocked, model.StatusDeferred, model.StatusCancelled,
			model.StatusInvalidated,
		}

		counts := make(map[string]int, len(statuses))
		total := 0

		for _, s := range statuses {
			filter := store.NodeFilter{Status: []model.Status{s}, Under: p.Under}
			_, count, err := st.ListNodes(ctx, filter, store.ListOptions{Limit: 0})
			if err != nil {
				return nil, fmt.Errorf("count %s nodes: %w", s, err)
			}
			counts[string(s)] = count
			total += count
		}
		counts["total"] = total

		data, _ := json.MarshalIndent(counts, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerProgressTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_progress",
		Description: "Get progress details for a node and its children",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id": {Type: "string", Description: "Node ID"},
			},
			Required: []string{"id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct{ ID string `json:"id"` }
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse progress args: %w", err)
		}

		node, err := st.GetNode(ctx, p.ID)
		if err != nil {
			return nil, err
		}

		children, err := st.GetDirectChildren(ctx, p.ID)
		if err != nil {
			return nil, err
		}

		type childProgress struct {
			ID       string  `json:"id"`
			Title    string  `json:"title"`
			Status   string  `json:"status"`
			Progress float64 `json:"progress"`
		}

		childList := make([]childProgress, 0, len(children))
		for _, c := range children {
			childList = append(childList, childProgress{
				ID:       c.ID,
				Title:    c.Title,
				Status:   string(c.Status),
				Progress: c.Progress,
			})
		}

		result := map[string]any{
			"id":       node.ID,
			"title":    node.Title,
			"progress": node.Progress,
			"status":   string(node.Status),
			"children": childList,
		}

		data, _ := json.MarshalIndent(result, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerStaleTool(reg *ToolRegistry, agentSvc *service.AgentService, configSvc *service.ConfigService) {
	reg.Register(ToolDef{
		Name:        "mtix_stale",
		Description: "List agents with stale heartbeats",
		InputSchema: SchemaObj{Type: "object"},
	}, func(ctx context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
		threshold := configSvc.AgentStaleThreshold()
		agents, err := agentSvc.GetStaleAgents(ctx, threshold)
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(agents, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerOrphansTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_orphans",
		Description: "List root-level nodes (no parent)",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"limit": {Type: "number", Description: "Max results (default 50)"},
			},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct{ Limit int `json:"limit"` }
		if args != nil {
			_ = json.Unmarshal(args, &p)
		}
		if p.Limit == 0 {
			p.Limit = 50
		}

		// Root nodes are those with no "." in their ID (top-level project nodes).
		// We use ListNodes with no Under filter and post-filter for root-level.
		nodes, _, err := st.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: p.Limit})
		if err != nil {
			return nil, err
		}

		// Filter to only root-level nodes (no parent).
		var roots []*model.Node
		for _, n := range nodes {
			if n.ParentID == "" {
				roots = append(roots, n)
			}
		}

		data, _ := json.MarshalIndent(map[string]any{
			"nodes": roots,
			"count": len(roots),
		}, "", "  ")
		return SuccessResult(string(data)), nil
	})
}
