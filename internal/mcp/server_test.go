// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
)

// TestServer_Initialize_ReturnsCapabilities verifies MCP handshake.
func TestServer_Initialize_ReturnsCapabilities(t *testing.T) {
	input := makeRequest(t, 1, "initialize", mcp.InitializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      mcp.ClientInfo{Name: "test-client", Version: "1.0"},
	})

	output := runServer(t, input)
	resp := parseResponse(t, output)

	assert.Nil(t, resp.Error)
	require.NotNil(t, resp.Result)

	var result mcp.InitializeResult
	data, _ := json.Marshal(resp.Result)
	require.NoError(t, json.Unmarshal(data, &result))

	assert.Equal(t, "2024-11-05", result.ProtocolVersion)
	assert.Equal(t, "mtix", result.ServerInfo.Name)
	assert.NotNil(t, result.Capabilities.Tools)
}

// TestServer_Ping_ReturnsEmpty verifies ping handling.
func TestServer_Ping_ReturnsEmpty(t *testing.T) {
	input := makeRequest(t, 2, "ping", nil)
	output := runServer(t, input)
	resp := parseResponse(t, output)
	assert.Nil(t, resp.Error)
}

// TestServer_ToolsList_ReturnsRegisteredTools verifies tool listing.
func TestServer_ToolsList_ReturnsRegisteredTools(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	input := makeRequest(t, 3, "tools/list", nil)
	var output bytes.Buffer
	srv := mcp.NewServer(strings.NewReader(input), &output, logger, "test")

	// Register a test tool.
	srv.Registry().Register(mcp.ToolDef{
		Name:        "mtix_test",
		Description: "A test tool",
		InputSchema: mcp.SchemaObj{Type: "object"},
	}, func(_ context.Context, _ json.RawMessage) (*mcp.ToolsCallResult, error) {
		return mcp.SuccessResult("ok"), nil
	})

	err := srv.Serve(context.Background())
	require.NoError(t, err)

	resp := parseResponse(t, output.String())
	assert.Nil(t, resp.Error)

	var result mcp.ToolsListResult
	data, _ := json.Marshal(resp.Result)
	require.NoError(t, json.Unmarshal(data, &result))
	require.Len(t, result.Tools, 1)
	assert.Equal(t, "mtix_test", result.Tools[0].Name)
}

// TestServer_ToolsCall_InvokesHandler verifies tool invocation.
func TestServer_ToolsCall_InvokesHandler(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	input := makeRequest(t, 4, "tools/call", mcp.ToolsCallParams{
		Name:      "mtix_echo",
		Arguments: json.RawMessage(`{"text":"hello"}`),
	})

	var output bytes.Buffer
	srv := mcp.NewServer(strings.NewReader(input), &output, logger, "test")

	srv.Registry().Register(mcp.ToolDef{
		Name:        "mtix_echo",
		Description: "Echo tool",
		InputSchema: mcp.SchemaObj{Type: "object"},
	}, func(_ context.Context, args json.RawMessage) (*mcp.ToolsCallResult, error) {
		var p struct{ Text string `json:"text"` }
		_ = json.Unmarshal(args, &p)
		return mcp.SuccessResult(p.Text), nil
	})

	err := srv.Serve(context.Background())
	require.NoError(t, err)

	resp := parseResponse(t, output.String())
	assert.Nil(t, resp.Error)

	var result mcp.ToolsCallResult
	data, _ := json.Marshal(resp.Result)
	require.NoError(t, json.Unmarshal(data, &result))
	require.Len(t, result.Content, 1)
	assert.Equal(t, "hello", result.Content[0].Text)
	assert.False(t, result.IsError)
}

// TestServer_UnknownMethod_ReturnsError verifies error handling.
func TestServer_UnknownMethod_ReturnsError(t *testing.T) {
	input := makeRequest(t, 5, "unknown/method", nil)
	output := runServer(t, input)
	resp := parseResponse(t, output)

	require.NotNil(t, resp.Error)
	assert.Equal(t, mcp.ErrCodeMethodNotFound, resp.Error.Code)
}

// TestServer_UnknownTool_ReturnsToolError verifies unknown tool handling.
func TestServer_UnknownTool_ReturnsToolError(t *testing.T) {
	input := makeRequest(t, 6, "tools/call", mcp.ToolsCallParams{
		Name: "nonexistent",
	})
	output := runServer(t, input)
	resp := parseResponse(t, output)

	// Tool errors are returned as successful responses with isError=true.
	assert.Nil(t, resp.Error)
	var result mcp.ToolsCallResult
	data, _ := json.Marshal(resp.Result)
	require.NoError(t, json.Unmarshal(data, &result))
	assert.True(t, result.IsError)
}

// TestServer_InvalidJSON_ReturnsParseError verifies parse error handling.
func TestServer_InvalidJSON_ReturnsParseError(t *testing.T) {
	output := runServer(t, "not json at all\n")
	resp := parseResponse(t, output)
	require.NotNil(t, resp.Error)
	assert.Equal(t, mcp.ErrCodeParse, resp.Error.Code)
}

// Helper functions.

func makeRequest(t *testing.T, id int, method string, params any) string {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		data, err := json.Marshal(params)
		require.NoError(t, err)
		req["params"] = json.RawMessage(data)
	}
	line, err := json.Marshal(req)
	require.NoError(t, err)
	return string(line) + "\n"
}

func runServer(t *testing.T, input string) string {
	t.Helper()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	var output bytes.Buffer
	srv := mcp.NewServer(strings.NewReader(input), &output, logger, "test")

	err := srv.Serve(context.Background())
	require.NoError(t, err)

	return output.String()
}

func parseResponse(t *testing.T, output string) mcp.Response {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	require.NotEmpty(t, lines, "no output from server")

	var resp mcp.Response
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &resp))
	return resp
}
