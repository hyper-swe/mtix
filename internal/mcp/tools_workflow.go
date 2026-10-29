// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// RegisterWorkflowTools registers workflow MCP tools per MTIX-6.2.2.
func RegisterWorkflowTools(reg *ToolRegistry, nodeSvc *service.NodeService, bgSvc *service.BackgroundService, opts ...ToolOption) {
	cfg := applyToolOptions(opts)
	registerClaimTool(reg, nodeSvc)
	registerUnclaimTool(reg, nodeSvc, cfg.author)
	registerDoneTool(reg, nodeSvc)
	registerDeferTool(reg, nodeSvc, cfg.author)
	registerCancelTool(reg, nodeSvc)
	registerReopenTool(reg, nodeSvc)
	registerReadyTool(reg, bgSvc)
	registerBlockedTool(reg, nodeSvc)
	registerSearchTool(reg, nodeSvc, cfg.primaryProject)
	registerRerunTool(reg, nodeSvc)
}

func registerClaimTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_claim",
		Description: "Claim a node for an agent",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":       {Type: "string", Description: "Node ID"},
				"agent_id": {Type: "string", Description: "Agent ID; nonempty raw IDs: max 64 UTF-8 bytes, no whitespace-only, control or invisible format characters"},
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

		if err := svc.ClaimNode(ctx, p.ID, p.AgentID); err != nil {
			return nil, err
		}

		return SuccessResult(textClaimed(p.ID, p.AgentID)), nil
	})
}

// registerUnclaimTool registers mtix_unclaim (FR-10.4). The actor is agent_id
// when given, otherwise the server process identity (WithAuthor); only the
// assignee may release a claim unless force is set (MTIX-169).
func registerUnclaimTool(reg *ToolRegistry, svc *service.NodeService, author string) {
	reg.Register(ToolDef{
		Name:        "mtix_unclaim",
		Description: "Release a node assignment. Only the assignee can release it unless force is true.",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":       {Type: "string", Description: "Node ID"},
				"reason":   {Type: "string", Description: "Reason for release"},
				"agent_id": {Type: "string", Description: "Acting agent ID; defaults to the server's MTIX_AUTHOR_ID or author_id"},
				"force":    {Type: "boolean", Description: "Release a claim held by another agent; the activity log names you, the previous assignee and the reason"},
			},
			Required: []string{"id", "reason"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID      string `json:"id"`
			Reason  string `json:"reason"`
			AgentID string `json:"agent_id"`
			Force   bool   `json:"force"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse unclaim args: %w", err)
		}
		if p.AgentID == "" {
			p.AgentID = author
		}

		if err := svc.UnclaimNode(ctx, p.ID, p.Reason, p.AgentID, p.Force); err != nil {
			return nil, err
		}

		return SuccessResult(textUnclaimed(p.ID)), nil
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
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse done args: %w", err)
		}

		if err := svc.TransitionStatus(ctx, p.ID, model.StatusDone, "marked done", "mcp"); err != nil {
			return nil, err
		}

		return SuccessResult(textDone(p.ID)), nil
	})
}

// WithAuthor sets the author identity the MCP tools record for this server
// process (MTIX-95.22). `mtix mcp` passes its process identity (MTIX-24) only
// when MTIX_AUTHOR_ID or the author_id config key sets one; an empty value
// keeps the "mcp" default. Today mtix_defer records it.
func WithAuthor(author string) ToolOption {
	return func(c *toolConfig) { c.author = author }
}

// registerDeferTool registers mtix_defer (FR-3.8, FR-3.8b). An `until` is
// parsed here at the boundary: an unparsable value is rejected as
// ErrInvalidInput before anything changes, and a valid one is stored with the
// transition by NodeService.DeferNode (MTIX-95.22). The author is the server
// process identity when one is wired (WithAuthor), "mcp" otherwise.
func registerDeferTool(reg *ToolRegistry, svc *service.NodeService, author string) {
	if author == "" {
		author = "mcp"
	}
	reg.Register(ToolDef{
		Name:        "mtix_defer",
		Description: "Defer a node, optionally with a wake time (until). Claims are refused until the wake time passes; the next background pass reopens the node after it. Omitting until clears any earlier wake time.",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":    {Type: "string", Description: "Node ID"},
				"until": {Type: "string", Description: "Wake time, RFC 3339 with a zone (e.g. 2026-04-01T00:00:00Z); omit for no wake time"},
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

		wake, err := service.ParseDeferUntil(p.Until)
		if err != nil {
			return nil, err
		}
		if err := svc.DeferNode(ctx, p.ID, wake, "deferred", author); err != nil {
			return nil, err
		}

		if wake != nil {
			return SuccessResult(textDeferred(p.ID, wake.Format(time.RFC3339))), nil
		}
		return SuccessResult(textDeferred(p.ID, "")), nil
	})
}

func registerCancelTool(reg *ToolRegistry, svc *service.NodeService) {
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

		if err := svc.CancelNode(ctx, p.ID, p.Reason, "mcp", p.Cascade); err != nil {
			return nil, err
		}

		return SuccessResult(textCancelled(p.ID, p.Reason)), nil
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
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse reopen args: %w", err)
		}

		if err := svc.TransitionStatus(ctx, p.ID, model.StatusOpen, "reopened", "mcp"); err != nil {
			return nil, err
		}

		return SuccessResult(textReopened(p.ID)), nil
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

func registerBlockedTool(reg *ToolRegistry, svc *service.NodeService) {
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
		var p struct {
			ID string `json:"id"`
		}
		if args != nil {
			_ = json.Unmarshal(args, &p)
		}

		if p.ID != "" {
			blockers, err := service.NewDependencyServiceFromNodeService(svc).GetBlockers(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			data, _ := json.MarshalIndent(blockers, "", "  ")
			return SuccessResult(string(data)), nil
		}

		// List all blocked nodes.
		nodes, _, err := svc.ListNodes(ctx, service.NodeFilter{
			Status: []model.Status{model.StatusBlocked},
		}, service.ListOptions{Limit: 50})
		if err != nil {
			return nil, err
		}
		data, _ := json.MarshalIndent(nodes, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerSearchTool(reg *ToolRegistry, svc *service.NodeService, primaryProject string) {
	reg.Register(ToolDef{
		Name:        "mtix_search",
		Description: "Search nodes by text and filters",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"query":    {Type: "string", Description: "Words that must ALL appear (case-insensitive for ASCII letters only; literal) in title, description or prompt"},
				"status":   {Type: "string", Description: "Filter by status"},
				"assignee": {Type: "string", Description: "Filter by assignee"},
				"under":    {Type: "string", Description: "Filter by subtree"},
				"project":  {Type: "string", Description: "Scope to a project prefix; omit for the primary project, 'all' to span every project"},
				"limit":    {Type: "number", Description: "Max results (default 50)"},
			},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			Query    string `json:"query"`
			Status   string `json:"status"`
			Assignee string `json:"assignee"`
			Under    string `json:"under"`
			Project  string `json:"project"`
			Limit    int    `json:"limit"`
		}
		if err := decodeStrictArgs(args, &p); err != nil {
			return nil, err
		}
		if p.Limit == 0 {
			p.Limit = 50
		}

		filter := service.NodeFilter{
			Project: resolveScopeProject(p.Project, primaryProject),
			Text:    service.SplitSearchText(p.Query),
		}
		if p.Query != "" && len(filter.Text) == 0 {
			filter.Text = []string{p.Query} // blank query: let the service reject it
		}
		if p.Under != "" {
			filter.Under = []string{p.Under}
		}
		if p.Assignee != "" {
			filter.Assignee = []string{p.Assignee}
		}
		if p.Status != "" {
			filter.Status = []model.Status{model.Status(p.Status)}
		}

		nodes, total, err := svc.ListNodes(ctx, filter, service.ListOptions{Limit: p.Limit})
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

		return SuccessResult(textRerun(p.ID, p.Strategy)), nil
	})
}
