// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// RegisterDepTools registers dependency management MCP tools per FR-4/FR-14.
// A supplied service shares the application event publisher and injected clock.
// Omitted services retain compatibility with standalone tool registrations.
func RegisterDepTools(reg *ToolRegistry, st store.Store, services ...*service.DependencyService) {
	svc := service.NewDependencyService(st, nil, nil, time.Now)
	if len(services) > 0 && services[0] != nil {
		svc = services[0]
	}
	registerDepAddTool(reg, svc)
	registerDepRemoveTool(reg, svc)
	registerDepShowTool(reg, st)
}

// dependencyTypeSchema advertises the canonical dependency types per FR-4.2.
func dependencyTypeSchema() SchemaProp {
	depTypes := model.AllDepTypes()
	values := make([]string, 0, len(depTypes))
	for _, depType := range depTypes {
		values = append(values, string(depType))
	}
	return SchemaProp{Type: "string", Description: "Dependency type: " + strings.Join(values, ", "), Enum: values}
}

func registerDepAddTool(reg *ToolRegistry, svc *service.DependencyService) {
	reg.Register(ToolDef{
		Name:        "mtix_dep_add",
		Description: "Add a dependency between two nodes",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"from_id":  {Type: "string", Description: "Source node ID (the blocked node)"},
				"to_id":    {Type: "string", Description: "Target node ID (the blocker)"},
				"dep_type": dependencyTypeSchema(),
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

		if err := svc.AddDependency(ctx, dep); err != nil {
			return nil, err
		}

		return SuccessResult(fmt.Sprintf("Dependency added: %s -[%s]-> %s", p.FromID, p.DepType, p.ToID)), nil
	})
}

func registerDepRemoveTool(reg *ToolRegistry, svc *service.DependencyService) {
	reg.Register(ToolDef{
		Name:        "mtix_dep_remove",
		Description: "Remove a dependency between two nodes",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"from_id":  {Type: "string", Description: "Source node ID"},
				"to_id":    {Type: "string", Description: "Target node ID"},
				"dep_type": dependencyTypeSchema(),
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

		if err := svc.RemoveDependency(ctx, p.FromID, p.ToID, model.DepType(p.DepType)); err != nil {
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
		var p struct {
			ID string `json:"id"`
		}
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
