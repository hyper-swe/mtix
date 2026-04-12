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

// RegisterNodeTools registers node management MCP tools per MTIX-6.2.1.
func RegisterNodeTools(reg *ToolRegistry, nodeSvc *service.NodeService, st store.Store) {
	registerCreateTool(reg, nodeSvc)
	registerShowTool(reg, nodeSvc)
	registerListTool(reg, st)
	registerDeleteTool(reg, nodeSvc)
	registerUndeleteTool(reg, nodeSvc)
	registerDecomposeTool(reg, nodeSvc)
	registerUpdateTool(reg, nodeSvc)
}

func registerCreateTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_create",
		Description: "Create a new node in the task hierarchy",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"title":       {Type: "string", Description: "Node title (required)"},
				"parent_id":   {Type: "string", Description: "Parent node ID (empty for root)"},
				"project":     {Type: "string", Description: "Project prefix"},
				"description": {Type: "string", Description: "Node description"},
				"prompt":      {Type: "string", Description: "Prompt text for LLM agents"},
				"acceptance":  {Type: "string", Description: "Acceptance criteria"},
				"priority":    {Type: "number", Description: "Priority 1-5 (1=critical)"},
			},
			Required: []string{"title", "project"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			Title       string `json:"title"`
			ParentID    string `json:"parent_id"`
			Project     string `json:"project"`
			Description string `json:"description"`
			Prompt      string `json:"prompt"`
			Acceptance  string `json:"acceptance"`
			Priority    int    `json:"priority"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse create args: %w", err)
		}

		req := &service.CreateNodeRequest{
			ParentID:    p.ParentID,
			Project:     p.Project,
			Title:       p.Title,
			Description: p.Description,
			Prompt:      p.Prompt,
			Acceptance:  p.Acceptance,
			Creator:     "mcp",
			Priority:    model.Priority(p.Priority),
		}
		if req.Priority == 0 {
			req.Priority = model.PriorityMedium
		}

		node, err := svc.CreateNode(ctx, req)
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(node, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerShowTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_show",
		Description: "Show full details of a node",
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
			return nil, fmt.Errorf("parse show args: %w", err)
		}

		node, err := svc.GetNode(ctx, p.ID)
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(node, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerListTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_list",
		Description: "List nodes with filtering and pagination",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"status":   {Type: "string", Description: "Filter by status"},
				"under":    {Type: "string", Description: "Filter by parent subtree"},
				"assignee": {Type: "string", Description: "Filter by assignee"},
				"limit":    {Type: "number", Description: "Max results (default 50)"},
				"offset":   {Type: "number", Description: "Pagination offset"},
			},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			Status   string `json:"status"`
			Under    string `json:"under"`
			Assignee string `json:"assignee"`
			Limit    int    `json:"limit"`
			Offset   int    `json:"offset"`
		}
		if args != nil {
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, fmt.Errorf("parse list args: %w", err)
			}
		}

		if p.Limit == 0 {
			p.Limit = 50
		}

		filter := store.NodeFilter{}
		if p.Under != "" {
			filter.Under = []string{p.Under}
		}
		if p.Assignee != "" {
			filter.Assignee = []string{p.Assignee}
		}
		if p.Status != "" {
			filter.Status = []model.Status{model.Status(p.Status)}
		}

		nodes, total, err := st.ListNodes(ctx, filter, store.ListOptions{
			Limit: p.Limit, Offset: p.Offset,
		})
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(map[string]any{
			"nodes": nodes, "total": total,
		}, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerDeleteTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_delete",
		Description: "Soft-delete a node",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":      {Type: "string", Description: "Node ID"},
				"cascade": {Type: "boolean", Description: "Delete descendants too"},
			},
			Required: []string{"id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID      string `json:"id"`
			Cascade bool   `json:"cascade"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse delete args: %w", err)
		}

		if err := svc.DeleteNode(ctx, p.ID, p.Cascade, "mcp"); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Deleted %s", p.ID)), nil
	})
}

func registerUndeleteTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_undelete",
		Description: "Restore a soft-deleted node",
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
			return nil, fmt.Errorf("parse undelete args: %w", err)
		}

		if err := svc.UndeleteNode(ctx, p.ID); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Undeleted %s", p.ID)), nil
	})
}

func registerDecomposeTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_decompose",
		Description: "Create multiple child nodes atomically",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"parent_id": {Type: "string", Description: "Parent node ID"},
				"children":  {Type: "string", Description: "JSON array of {title, prompt, acceptance}"},
			},
			Required: []string{"parent_id", "children"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ParentID string                    `json:"parent_id"`
			Children []service.DecomposeInput  `json:"children"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse decompose args: %w", err)
		}

		ids, err := svc.Decompose(ctx, p.ParentID, p.Children, "mcp")
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(map[string]any{
			"parent": p.ParentID, "created": ids,
		}, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerUpdateTool(reg *ToolRegistry, svc *service.NodeService) {
	reg.Register(ToolDef{
		Name:        "mtix_update",
		Description: "Update a node's fields",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":          {Type: "string", Description: "Node ID (required)"},
				"title":       {Type: "string", Description: "New title"},
				"description": {Type: "string", Description: "New description"},
				"priority":    {Type: "number", Description: "New priority (1-5)"},
			},
			Required: []string{"id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Priority    int    `json:"priority"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse update args: %w", err)
		}

		updates := &store.NodeUpdate{}
		if p.Title != "" {
			updates.Title = &p.Title
		}
		if p.Description != "" {
			updates.Description = &p.Description
		}
		if p.Priority > 0 {
			pri := model.Priority(p.Priority)
			updates.Priority = &pri
		}

		if err := svc.UpdateNode(ctx, p.ID, updates); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Updated %s", p.ID)), nil
	})
}
