// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// registerCreateTool describes classification and explicit atomic assignment (FR-3.1/FR-10.4).
func registerCreateTool(reg *ToolRegistry, svc *service.NodeService, primaryProject string) {
	reg.Register(createToolDefinition(), func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		return callCreateTool(ctx, svc, primaryProject, args)
	})
}

// createToolDefinition keeps work classification separate from hierarchy and authorship.
func createToolDefinition() ToolDef {
	return ToolDef{
		Name: "mtix_create", Description: "Create a new node in the task hierarchy",
		InputSchema: SchemaObj{Type: "object", Required: []string{"title"}, Properties: map[string]SchemaProp{
			"title":       {Type: "string", Description: "Node title (required)"},
			"parent_id":   {Type: "string", Description: "Parent node ID (empty for root)"},
			"project":     {Type: "string", Description: "Project prefix (optional; defaults to the primary project)"},
			"description": {Type: "string", Description: "Node description"},
			"prompt":      {Type: "string", Description: "Prompt text for LLM agents"},
			"acceptance":  {Type: "string", Description: "Acceptance criteria"},
			"priority":    {Type: "number", Description: "Priority 1-5 (1=critical)"},
			"issue_type":  {Type: "string", Description: "Work classification (omitted = unset)", Enum: issueTypeEnums()},
			"assignee":    {Type: "string", Description: "Atomically claim the new node for this agent/user; creator is independent"},
			"creator":     {Type: "string", Description: "Author of the new node (defaults to mcp)"},
		}},
	}
}

// createToolArgs keeps the MCP surface limited to its advertised create fields.
type createToolArgs struct {
	Title       string          `json:"title"`
	ParentID    string          `json:"parent_id"`
	Project     string          `json:"project"`
	Description string          `json:"description"`
	Prompt      string          `json:"prompt"`
	Acceptance  string          `json:"acceptance"`
	Priority    model.Priority  `json:"priority"`
	IssueType   model.IssueType `json:"issue_type"`
	Assignee    string          `json:"assignee"`
	Creator     string          `json:"creator"`
}

// callCreateTool routes classification and assignment through the service's atomic boundary.
func callCreateTool(ctx context.Context, svc *service.NodeService, primaryProject string, args json.RawMessage) (*ToolsCallResult, error) {
	var argsIn createToolArgs
	if err := json.Unmarshal(args, &argsIn); err != nil {
		return nil, fmt.Errorf("parse create args: %w", err)
	}
	req := service.CreateNodeRequest{Title: argsIn.Title, ParentID: argsIn.ParentID, Project: argsIn.Project, Description: argsIn.Description, Prompt: argsIn.Prompt, Acceptance: argsIn.Acceptance, Priority: argsIn.Priority, IssueType: argsIn.IssueType, Assignee: argsIn.Assignee, Creator: argsIn.Creator}
	if req.Project == "" {
		req.Project = primaryProject
	}
	if req.Creator == "" {
		req.Creator = "mcp"
	}
	if req.Priority == 0 {
		req.Priority = model.PriorityMedium
	}
	node, err := svc.CreateNode(ctx, &req)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(node, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal created node: %w", err)
	}
	return SuccessResult(string(data)), nil
}

// issueTypeEnums exposes the canonical FR-3.1 classification list to MCP callers.
func issueTypeEnums() []string {
	values := make([]string, 0, len(model.AllIssueTypes()))
	for _, kind := range model.AllIssueTypes() {
		values = append(values, string(kind))
	}
	return values
}
