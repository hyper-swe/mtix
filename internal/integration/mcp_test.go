// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// setupMCPEnv creates a real MCP server with real store and services.
func setupMCPEnv(t *testing.T) (*mcp.Server, *mcp.ToolRegistry) {
	t.Helper()

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	st, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	broadcaster := &service.NoopBroadcaster{}
	config := &service.StaticConfig{}
	nodeSvc := service.NewNodeService(st, broadcaster, config, logger, testClock())
	bgSvc := service.NewBackgroundService(st, config, logger, testClock())
	promptSvc := service.NewPromptService(st, broadcaster, logger, testClock())
	ctxSvc := service.NewContextService(st, config, logger)
	agentSvc := service.NewAgentService(st, broadcaster, config, logger, testClock())
	sessionSvc := service.NewSessionService(st, config, logger, testClock())
	configSvc, err := service.NewConfigService("")
	require.NoError(t, err)

	reg := mcp.NewToolRegistry()
	mcp.RegisterNodeTools(reg, nodeSvc, st)
	mcp.RegisterWorkflowTools(reg, nodeSvc, st, bgSvc)
	mcp.RegisterContextTools(reg, ctxSvc, promptSvc)
	mcp.RegisterDepTools(reg, st)
	mcp.RegisterSessionTools(reg, sessionSvc, agentSvc)
	mcp.RegisterAnalyticsTools(reg, st, agentSvc, configSvc)
	mcp.RegisterDocsTools(reg)

	var input bytes.Buffer
	var output bytes.Buffer
	server := mcp.NewServer(&input, &output, logger, "test")

	return server, reg
}

// TestMCP_Integration_ToolsList_ReturnsAllTools verifies all tools are registered.
func TestMCP_Integration_ToolsList_ReturnsAllTools(t *testing.T) {
	_, reg := setupMCPEnv(t)

	tools := reg.List()
	assert.GreaterOrEqual(t, len(tools), 30, "should have at least 30 registered tools")

	// Verify some key tools exist.
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}

	expected := []string{
		"mtix_create", "mtix_show", "mtix_list", "mtix_done",
		"mtix_claim", "mtix_context", "mtix_search", "mtix_ready",
		"mtix_session_start", "mtix_agent_heartbeat", "mtix_discover",
	}
	for _, name := range expected {
		assert.True(t, names[name], "tool %s should be registered", name)
	}
}

// TestMCP_Integration_CreateAndShowNode verifies create → show flow via MCP.
func TestMCP_Integration_CreateAndShowNode(t *testing.T) {
	_, reg := setupMCPEnv(t)
	ctx := context.Background()

	// Create a node.
	createArgs, _ := json.Marshal(map[string]any{
		"title":   "MCP Integration Test",
		"project": "MCP",
	})
	result, err := reg.Call(ctx, "mtix_create", createArgs)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	// Extract node ID from result.
	var createResp map[string]any
	err = json.Unmarshal([]byte(result.Content[0].Text), &createResp)
	require.NoError(t, err)
	nodeID, ok := createResp["id"].(string)
	require.True(t, ok, "response should contain node ID")

	// Show the node.
	showArgs, _ := json.Marshal(map[string]string{"id": nodeID})
	result, err = reg.Call(ctx, "mtix_show", showArgs)
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "MCP Integration Test")
}

// TestMCP_Integration_SearchNodes verifies search tool works with real store.
func TestMCP_Integration_SearchNodes(t *testing.T) {
	_, reg := setupMCPEnv(t)
	ctx := context.Background()

	// Create nodes.
	for _, title := range []string{"Task Alpha", "Task Beta", "Task Gamma"} {
		args, _ := json.Marshal(map[string]any{
			"title":   title,
			"project": "SRCH",
		})
		_, err := reg.Call(ctx, "mtix_create", args)
		require.NoError(t, err)
	}

	// Search all.
	searchArgs, _ := json.Marshal(map[string]any{"limit": 10})
	result, err := reg.Call(ctx, "mtix_search", searchArgs)
	require.NoError(t, err)
	require.False(t, result.IsError)

	var searchResp map[string]any
	err = json.Unmarshal([]byte(result.Content[0].Text), &searchResp)
	require.NoError(t, err)

	total, ok := searchResp["total"].(float64)
	require.True(t, ok)
	assert.GreaterOrEqual(t, int(total), 3)
}

// TestMCP_Integration_DiscoverTool verifies discover returns all tools.
func TestMCP_Integration_DiscoverTool(t *testing.T) {
	_, reg := setupMCPEnv(t)
	ctx := context.Background()

	result, err := reg.Call(ctx, "mtix_discover", nil)
	require.NoError(t, err)
	require.False(t, result.IsError)

	var resp map[string]any
	err = json.Unmarshal([]byte(result.Content[0].Text), &resp)
	require.NoError(t, err)

	count, ok := resp["count"].(float64)
	require.True(t, ok)
	assert.GreaterOrEqual(t, int(count), 30)
}
