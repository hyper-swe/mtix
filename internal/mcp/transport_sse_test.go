// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMCP_SSE_ConnectAndCallTool verifies tool call over SSE transport.
func TestMCP_SSE_ConnectAndCallTool(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()

	reg.Register(ToolDef{
		Name:        "echo",
		Description: "Echo back",
		InputSchema: SchemaObj{Type: "object"},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		return SuccessResult("hello"), nil
	})

	sseServer := NewSSEServer(reg, logger, "test")

	// Build a tools/call request.
	callReq := Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  MethodToolsCall,
		Params:  json.RawMessage(`{"name":"echo","arguments":{}}`),
	}
	reqData, _ := json.Marshal(callReq)

	req := httptest.NewRequest(http.MethodPost, "/mcp/sse", bytes.NewReader(reqData))
	w := httptest.NewRecorder()

	sseServer.HandleSSE(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body := w.Body.String()
	assert.Contains(t, body, "event: message")
	// Response is JSON-RPC, extract the result field from the JSON.
	assert.Contains(t, body, "\"content\":")
	assert.Contains(t, body, "hello")
}

// TestMCP_SSE_InitializeReturnsCapabilities verifies initialize over SSE.
func TestMCP_SSE_InitializeReturnsCapabilities(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	initReq := Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  MethodInitialize,
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	}
	reqData, _ := json.Marshal(initReq)

	req := httptest.NewRequest(http.MethodPost, "/mcp/sse", bytes.NewReader(reqData))
	w := httptest.NewRecorder()

	sseServer.HandleSSE(w, req)

	body := w.Body.String()
	// Response is SSE format: "event: message\ndata: {json-rpc response}\n\n"
	assert.Contains(t, body, "event: message")
	assert.Contains(t, body, "mtix")
	assert.Contains(t, body, "2024-11-05")
}

// TestMCP_SSE_UnknownTool_ReturnsError verifies error for unknown tool over SSE.
func TestMCP_SSE_UnknownTool_ReturnsError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "test")

	callReq := Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  MethodToolsCall,
		Params:  json.RawMessage(`{"name":"nonexistent","arguments":{}}`),
	}
	reqData, _ := json.Marshal(callReq)

	req := httptest.NewRequest(http.MethodPost, "/mcp/sse", bytes.NewReader(reqData))
	w := httptest.NewRecorder()

	sseServer.HandleSSE(w, req)

	body := w.Body.String()
	assert.Contains(t, body, "isError")
}

// TestMCP_SSE_InvalidJSON_ReturnsParseError verifies parse error handling.
func TestMCP_SSE_InvalidJSON_ReturnsParseError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "test")

	req := httptest.NewRequest(http.MethodPost, "/mcp/sse", strings.NewReader("{invalid json}"))
	w := httptest.NewRecorder()

	sseServer.HandleSSE(w, req)

	body := w.Body.String()
	assert.Contains(t, body, "parse error")
}

// TestMCP_SSE_BroadcastNotification verifies notifications are sent to clients.
func TestMCP_SSE_BroadcastNotification(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "test")

	assert.Equal(t, 0, sseServer.ClientCount())

	// BroadcastNotification with no clients should not panic.
	sseServer.BroadcastNotification("test/event", map[string]string{"key": "value"})
}

// TestMCP_SSE_Ping_ReturnsEmptyObject verifies ping over SSE.
func TestMCP_SSE_Ping_ReturnsEmptyObject(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "test")

	pingReq := Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`42`),
		Method:  MethodPing,
	}
	reqData, _ := json.Marshal(pingReq)

	req := httptest.NewRequest(http.MethodPost, "/mcp/sse", bytes.NewReader(reqData))
	w := httptest.NewRecorder()

	sseServer.HandleSSE(w, req)

	body := w.Body.String()
	require.Contains(t, body, "event: message")
}
