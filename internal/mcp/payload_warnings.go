// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Mutation-time wire-cap warning of MCP tool calls (MTIX-95.12). The local
// field limits allow more than the sync hub accepts (a prompt may be 100 KB,
// an event payload at most 64 KB); such a mutation succeeds, and `mtix sync
// push` holds its event. With a hub configured, each tool call runs with a
// sqlite.PayloadWarnings collector in its context, and every warning the
// store reported is added to a successful result as its own text block,
// after the tool's own content, so the agent sees it.

// SetPayloadWarnings turns the wire-cap warnings of tool calls on while
// enabled returns true (the CLI passes "a hub is configured"). Set once
// during MCP server setup, before Serve; nil turns them off.
func (r *ToolRegistry) SetPayloadWarnings(enabled func() bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.payloadWarnings = enabled
}

// callWithPayloadWarnings runs handler with a warning collector in its
// context when enabled reports a hub, and appends one text block per
// collected warning to a successful, non-error result. A failed call gets
// none: its mutation did not commit.
func callWithPayloadWarnings(ctx context.Context, enabled func() bool,
	handler ToolHandler, args json.RawMessage,
) (*ToolsCallResult, error) {
	if enabled == nil || !enabled() {
		return handler(ctx, args)
	}
	collected := &sqlite.PayloadWarnings{}
	result, err := handler(sqlite.WithPayloadWarnings(ctx, collected), args)
	if err != nil || result == nil || result.IsError {
		return result, err
	}
	for _, w := range collected.Drain() {
		result.Content = append(result.Content, TextContent(w.String()))
	}
	return result, nil
}
