// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// RegisterDepTools registers dependency management MCP tools per MTIX-6.2.4.
func RegisterDepTools(reg *ToolRegistry, st store.Store) {
	registerDepAddTool(reg, st)
	registerDepRemoveTool(reg, st)
	registerDepShowTool(reg, st)
}

func registerDepAddTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_dep_add",
		Description: "Add a dependency between two nodes",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"from_id":  {Type: "string", Description: "Source node ID (the blocked node)"},
				"to_id":    {Type: "string", Description: "Target node ID (the blocker)"},
				"dep_type": {Type: "string", Description: "Dependency type: blocks, needs_input, related", Enum: []string{"blocks", "needs_input", "related"}},
			},
			Required: []string{"from_id", "to_id", "dep_type"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			FromID  string `json:"from_id"`
			ToID    string `json:"to_id"`
			DepType string `json:"dep_type"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse dep_add args: %w", err)
		}

		dep := &model.Dependency{
			FromID:  p.FromID,
			ToID:    p.ToID,
			DepType: model.DepType(p.DepType),
		}

		if err := st.AddDependency(ctx, dep); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Dependency added: %s -[%s]-> %s", p.FromID, p.DepType, p.ToID)), nil
	})
}

func registerDepRemoveTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_dep_remove",
		Description: "Remove a dependency between two nodes",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"from_id":  {Type: "string", Description: "Source node ID"},
				"to_id":    {Type: "string", Description: "Target node ID"},
				"dep_type": {Type: "string", Description: "Dependency type: blocks, needs_input, related", Enum: []string{"blocks", "needs_input", "related"}},
			},
			Required: []string{"from_id", "to_id", "dep_type"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			FromID  string `json:"from_id"`
			ToID    string `json:"to_id"`
			DepType string `json:"dep_type"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse dep_remove args: %w", err)
		}

		if err := st.RemoveDependency(ctx, p.FromID, p.ToID, model.DepType(p.DepType)); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Dependency removed: %s -[%s]-> %s", p.FromID, p.DepType, p.ToID)), nil
	})
}

func registerDepShowTool(reg *ToolRegistry, st store.Store) {
	reg.Register(ToolDef{
		Name:        "mtix_dep_show",
		Description: "Show blocking dependencies for a node",
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
			return nil, fmt.Errorf("parse dep_show args: %w", err)
		}

		blockers, err := st.GetBlockers(ctx, p.ID)
		if err != nil {
			return nil, err
		}

		data, _ := json.MarshalIndent(blockers, "", "  ")
		return SuccessResult(string(data)), nil
	})
}
