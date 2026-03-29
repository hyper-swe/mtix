// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
)

// TestRegistry_Register_And_List verifies tool registration and listing.
func TestRegistry_Register_And_List(t *testing.T) {
	reg := mcp.NewToolRegistry()

	reg.Register(mcp.ToolDef{
		Name:        "tool_a",
		Description: "Tool A",
		InputSchema: mcp.SchemaObj{Type: "object"},
	}, noopHandler)

	reg.Register(mcp.ToolDef{
		Name:        "tool_b",
		Description: "Tool B",
		InputSchema: mcp.SchemaObj{Type: "object"},
	}, noopHandler)

	tools := reg.List()
	require.Len(t, tools, 2)
	assert.Equal(t, "tool_a", tools[0].Name)
	assert.Equal(t, "tool_b", tools[1].Name)
	assert.Equal(t, 2, reg.Count())
}

// TestRegistry_Call_InvokesHandler verifies tool invocation.
func TestRegistry_Call_InvokesHandler(t *testing.T) {
	reg := mcp.NewToolRegistry()

	called := false
	reg.Register(mcp.ToolDef{
		Name: "test_tool",
		InputSchema: mcp.SchemaObj{Type: "object"},
	}, func(_ context.Context, _ json.RawMessage) (*mcp.ToolsCallResult, error) {
		called = true
		return mcp.SuccessResult("done"), nil
	})

	result, err := reg.Call(context.Background(), "test_tool", nil)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Len(t, result.Content, 1)
	assert.Equal(t, "done", result.Content[0].Text)
}

// TestRegistry_Call_UnknownTool_ReturnsError verifies unknown tool handling.
func TestRegistry_Call_UnknownTool_ReturnsError(t *testing.T) {
	reg := mcp.NewToolRegistry()
	_, err := reg.Call(context.Background(), "nonexistent", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool")
}

// TestRegistry_Register_Duplicate_Panics verifies duplicate detection.
func TestRegistry_Register_Duplicate_Panics(t *testing.T) {
	reg := mcp.NewToolRegistry()

	reg.Register(mcp.ToolDef{
		Name: "dup_tool",
		InputSchema: mcp.SchemaObj{Type: "object"},
	}, noopHandler)

	assert.Panics(t, func() {
		reg.Register(mcp.ToolDef{
			Name: "dup_tool",
			InputSchema: mcp.SchemaObj{Type: "object"},
		}, noopHandler)
	})
}

// TestRegistry_List_PreservesOrder verifies registration order.
func TestRegistry_List_PreservesOrder(t *testing.T) {
	reg := mcp.NewToolRegistry()

	names := []string{"z_tool", "a_tool", "m_tool"}
	for _, name := range names {
		reg.Register(mcp.ToolDef{
			Name:        name,
			InputSchema: mcp.SchemaObj{Type: "object"},
		}, noopHandler)
	}

	tools := reg.List()
	for i, tool := range tools {
		assert.Equal(t, names[i], tool.Name)
	}
}

func noopHandler(_ context.Context, _ json.RawMessage) (*mcp.ToolsCallResult, error) {
	return mcp.SuccessResult("ok"), nil
}
