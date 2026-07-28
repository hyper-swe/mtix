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
	mu       sync.RWMutex
	tools    map[string]registeredTool
	order    []string // Preserves registration order for listing.
	readOnly bool     // MTIX-2.1.3: when true, only ScopeRead tools are listed/callable.
}

// SetReadOnly restricts this registry to read-scoped tools (MTIX-2.1.3). A
// read-only client cannot list or call write/admin tools. Set once during MCP
// server setup, before Serve.
func (r *ToolRegistry) SetReadOnly(ro bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readOnly = ro
}

// IsReadOnly reports whether the registry is in read-only mode.
func (r *ToolRegistry) IsReadOnly() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.readOnly
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

	// MTIX-2.1.3: classify the tool's access scope from its name unless the
	// caller set one explicitly. Unlisted tools default to write (fail-safe).
	if def.Scope == "" {
		def.Scope = scopeForTool(def.Name)
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
		def := r.tools[name].def
		// MTIX-2.1.3: a read-only client only sees read-scoped tools, so it
		// never attempts (and never discovers) a mutation it cannot perform.
		if r.readOnly && def.Scope != ScopeRead {
			continue
		}
		defs = append(defs, def)
	}
	return defs
}

// Call invokes a registered tool by name.
// Returns ErrMethodNotFound if the tool is not registered.
func (r *ToolRegistry) Call(ctx context.Context, name string, args json.RawMessage) (*ToolsCallResult, error) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	readOnly := r.readOnly
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}

	// MTIX-2.1.3: refuse a mutation on a read-only client — defense in depth
	// even though List hides these tools from a well-behaved client.
	if readOnly && tool.def.Scope != ScopeRead {
		return nil, fmt.Errorf("%w: tool %q needs %s scope but this client is read-only",
			ErrReadOnly, name, tool.def.Scope)
	}

	return tool.handler(ctx, args)
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}
