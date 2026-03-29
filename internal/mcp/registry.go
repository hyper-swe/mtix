// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// ToolHandler is the function signature for tool implementations.
// Receives the tool arguments as raw JSON and returns a result.
type ToolHandler func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error)

// registeredTool associates a tool definition with its handler.
type registeredTool struct {
	def     ToolDef
	handler ToolHandler
}

// ToolRegistry manages MCP tool registrations per MTIX-6.1.2.
// Thread-safe for concurrent reads after registration phase.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]registeredTool
	order []string // Preserves registration order for listing.
}

// NewToolRegistry creates an empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]registeredTool),
	}
}

// Register adds a tool to the registry.
// Panics if a tool with the same name is already registered (programming error).
func (r *ToolRegistry) Register(def ToolDef, handler ToolHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[def.Name]; exists {
		panic(fmt.Sprintf("duplicate MCP tool registration: %s", def.Name))
	}

	r.tools[def.Name] = registeredTool{def: def, handler: handler}
	r.order = append(r.order, def.Name)
}

// List returns all registered tool definitions in registration order.
func (r *ToolRegistry) List() []ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]ToolDef, 0, len(r.order))
	for _, name := range r.order {
		defs = append(defs, r.tools[name].def)
	}
	return defs
}

// Call invokes a registered tool by name.
// Returns ErrMethodNotFound if the tool is not registered.
func (r *ToolRegistry) Call(ctx context.Context, name string, args json.RawMessage) (*ToolsCallResult, error) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}

	return tool.handler(ctx, args)
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}
