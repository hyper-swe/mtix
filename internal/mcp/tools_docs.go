// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// DocGenerateFunc is a callback to invoke the actual doc generator.
// Returns a summary message describing what was generated.
type DocGenerateFunc func(force bool) (string, error)

// RegisterDocsTools registers documentation and discovery MCP tools per MTIX-6.2.7.
// An optional DocGenerateFunc wires mtix_docs_generate to the actual doc engine.
func RegisterDocsTools(reg *ToolRegistry, genFn ...DocGenerateFunc) {
	registerDiscoverTool(reg)

	var fn DocGenerateFunc
	if len(genFn) > 0 {
		fn = genFn[0]
	}
	registerDocsGenerateTool(reg, fn)
}

// registerDiscoverTool registers mtix_discover per FR-14.4.
// Returns a lightweight summary of all registered tools.
func registerDiscoverTool(reg *ToolRegistry) {
	reg.Register(ToolDef{
		Name:        "mtix_discover",
		Description: "List all available MCP tools with brief descriptions",
		InputSchema: SchemaObj{Type: "object"},
	}, func(_ context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
		tools := reg.List()

		type toolSummary struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}

		summaries := make([]toolSummary, 0, len(tools))
		for _, t := range tools {
			summaries = append(summaries, toolSummary{
				Name:        t.Name,
				Description: t.Description,
			})
		}

		data, _ := json.MarshalIndent(map[string]any{
			"tools": summaries,
			"count": len(summaries),
		}, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

// registerDocsGenerateTool registers mtix_docs_generate per FR-13.6.
// If genFn is provided, invokes the actual doc generator engine.
func registerDocsGenerateTool(reg *ToolRegistry, genFn DocGenerateFunc) {
	reg.Register(ToolDef{
		Name:        "mtix_docs_generate",
		Description: "Regenerate agent-facing documentation",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"force": {Type: "boolean", Description: "Force regeneration even if docs are current"},
			},
		},
	}, func(_ context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct{ Force bool `json:"force"` }
		if args != nil {
			_ = json.Unmarshal(args, &p)
		}

		// If a generator function is wired, invoke it.
		if genFn != nil {
			msg, err := genFn(p.Force)
			if err != nil {
				return nil, fmt.Errorf("docs generate: %w", err)
			}
			return SuccessResult(msg), nil
		}

		// Fallback: no generator wired (e.g., in tests).
		msg := "Documentation generation requested"
		if p.Force {
			msg = "Forced documentation regeneration requested"
		}

		return SuccessResult(msg), nil
	})
}
