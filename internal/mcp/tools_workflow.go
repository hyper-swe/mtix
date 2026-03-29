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

// RegisterWorkflowTools registers workflow MCP tools per MTIX-6.2.2.
func RegisterWorkflowTools(reg *ToolRegistry, nodeSvc *service.NodeService, st store.Store, bgSvc *service.BackgroundService) {
	registerClaimTool(reg, st)
	registerUnclaimTool(reg, st)
	registerDoneTool(reg, nodeSvc)
	registerDeferTool(reg, nodeSvc)
	registerCancelTool(reg, st)
	registerReopenTool(reg, nodeSvc)
	registerReadyTool(reg, bgSvc)
	registerBlockedTool(reg, st)
	registerSearchTool(reg, st)
	registerRerunTool(reg, nodeSvc)
}

func registerClaimTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_claim",
		Description: "Claim a node for an agent",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":       {Type: "string", Description: "Node ID"},
				"agent_id": {Type: "string", Description: "Agent ID"},
			},
			Required: []string{"id", "agent_id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID      string `json:"id"`
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse claim args: %w", err)
		}

		if err := st.ClaimNode(ctx, p.ID, p.AgentID); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Claimed %s for %s", p.ID, p.AgentID)), nil
	})
}

func registerUnclaimTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_unclaim",
		Description: "Release a node assignment",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":     {Type: "string", Description: "Node ID"},
				"reason": {Type: "string", Description: "Reason for release"},
			},
			Required: []string{"id", "reason"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse unclaim args: %w", err)
		}

		if err := st.UnclaimNode(ctx, p.ID, p.Reason, "mcp"); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Unclaimed %s", p.ID)), nil
	})
}

func registerDoneTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_done",
		Description: "Mark a node as done",
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
			return nil, fmt.Errorf("parse done args: %w", err)
		}

		if err := svc.TransitionStatus(ctx, p.ID, model.StatusDone, "marked done", "mcp"); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("%s → done", p.ID)), nil
	})
}

func registerDeferTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_defer",
		Description: "Defer a node",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":    {Type: "string", Description: "Node ID"},
				"until": {Type: "string", Description: "Defer until (ISO-8601)"},
			},
			Required: []string{"id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID    string `json:"id"`
			Until string `json:"until"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse defer args: %w", err)
		}

		if err := svc.TransitionStatus(ctx, p.ID, model.StatusDeferred, "deferred", "mcp"); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Deferred %s", p.ID)), nil
	})
}

func registerCancelTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_cancel",
		Description: "Cancel a node with mandatory reason",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":      {Type: "string", Description: "Node ID"},
				"reason":  {Type: "string", Description: "Cancellation reason"},
				"cascade": {Type: "boolean", Description: "Cancel descendants too"},
			},
			Required: []string{"id", "reason"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID      string `json:"id"`
			Reason  string `json:"reason"`
			Cascade bool   `json:"cascade"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse cancel args: %w", err)
		}

		if err := st.CancelNode(ctx, p.ID, p.Reason, "mcp", p.Cascade); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Cancelled %s: %s", p.ID, p.Reason)), nil
	})
}

func registerReopenTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_reopen",
		Description: "Reopen a closed node",
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
			return nil, fmt.Errorf("parse reopen args: %w", err)
		}

		if err := svc.TransitionStatus(ctx, p.ID, model.StatusOpen, "reopened", "mcp"); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Reopened %s", p.ID)), nil
	})
}

func registerReadyTool(reg *ToolRegistry, bgSvc *service.BackgroundService) {
	reg.Register(ToolDef{
		Name:        "mtix_ready",
		Description: "List nodes ready for work (unblocked, unassigned)",
		InputSchema: SchemaObj{Type: "object"},
	}, func(ctx context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
		nodes, err := bgSvc.GetReadyNodes(ctx)
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(nodes, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerBlockedTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_blocked",
		Description: "List blocked nodes with blocker details",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id": {Type: "string", Description: "Node ID (optional, shows blockers for specific node)"},
			},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct{ ID string `json:"id"` }
		if args != nil {
			_ = json.Unmarshal(args, &p)
		}

		if p.ID != "" {
			blockers, err := st.GetBlockers(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			data, _ := json.MarshalIndent(blockers, "", "  ")
			return SuccessResult(string(data)), nil
		}

		// List all blocked nodes.
		nodes, _, err := st.ListNodes(ctx, store.NodeFilter{
			Status: []model.Status{model.StatusBlocked},
		}, store.ListOptions{Limit: 50})
		if err != nil {
			return nil, err
		}
		data, _ := json.MarshalIndent(nodes, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerSearchTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_search",
		Description: "Search nodes with filters",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"status":   {Type: "string", Description: "Filter by status"},
				"assignee": {Type: "string", Description: "Filter by assignee"},
				"under":    {Type: "string", Description: "Filter by subtree"},
				"limit":    {Type: "number", Description: "Max results (default 50)"},
			},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			Status   string `json:"status"`
			Assignee string `json:"assignee"`
			Under    string `json:"under"`
			Limit    int    `json:"limit"`
		}
		if args != nil {
			_ = json.Unmarshal(args, &p)
		}
		if p.Limit == 0 {
			p.Limit = 50
		}

		filter := store.NodeFilter{Under: p.Under, Assignee: p.Assignee}
		if p.Status != "" {
			filter.Status = []model.Status{model.Status(p.Status)}
		}

		nodes, total, err := st.ListNodes(ctx, filter, store.ListOptions{Limit: p.Limit})
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(map[string]any{
			"nodes": nodes, "total": total,
		}, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerRerunTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_rerun",
		Description: "Invalidate and reprocess descendants of a node",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":       {Type: "string", Description: "Node ID"},
				"strategy": {Type: "string", Description: "Strategy: all, open_only, delete, review", Enum: []string{"all", "open_only", "delete", "review"}},
				"reason":   {Type: "string", Description: "Reason for rerun"},
			},
			Required: []string{"id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID       string `json:"id"`
			Strategy string `json:"strategy"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse rerun args: %w", err)
		}

		if p.Strategy == "" {
			p.Strategy = "all"
		}
		if p.Reason == "" {
			p.Reason = "rerun via MCP"
		}

		if err := svc.Rerun(ctx, p.ID, service.RerunStrategy(p.Strategy), p.Reason, "mcp"); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Rerun %s with strategy %s", p.ID, p.Strategy)), nil
	})
}
