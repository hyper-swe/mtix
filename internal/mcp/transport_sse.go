// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
)

// SSEServer implements MCP over Server-Sent Events per FR-14.1a.
// Allows multiple concurrent agent connections via HTTP SSE.
type SSEServer struct {
	tools   *ToolRegistry
	logger  *slog.Logger
	version string

	mu      sync.RWMutex
	clients map[*sseClient]struct{}
}

// sseClient represents a single SSE-connected agent.
type sseClient struct {
	w       http.ResponseWriter
	flusher http.Flusher
	done    chan struct{}
	mu      sync.Mutex
}

// NewSSEServer creates a new SSE transport server per FR-14.1a.
func NewSSEServer(tools *ToolRegistry, logger *slog.Logger, version string) *SSEServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &SSEServer{
		tools:   tools,
		logger:  logger,
		version: version,
		clients: make(map[*sseClient]struct{}),
	}
}

// HandleSSE is the HTTP handler for /mcp/sse endpoint.
// Clients POST JSON-RPC requests; responses and notifications are streamed as SSE.
func (s *SSEServer) HandleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	client := &sseClient{
		w:       w,
		flusher: flusher,
		done:    make(chan struct{}),
	}

	s.addClient(client)
	defer s.removeClient(client)

	// If it's a POST with a body, handle as a JSON-RPC request.
	if r.Method == http.MethodPost && r.Body != nil {
		s.handlePostRequests(r.Context(), r.Body, client)
		return
	}

	// For GET, keep connection open for notifications.
	<-r.Context().Done()
}

// handlePostRequests reads newline-delimited JSON-RPC from request body.
func (s *SSEServer) handlePostRequests(ctx context.Context, body io.Reader, client *sseClient) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		resp := s.processMessage(ctx, line)
		if resp != nil {
			if err := client.sendEvent("message", resp); err != nil {
				s.logger.Error("send SSE response", "error", err)
				return
			}
		}
	}
}

// processMessage handles a single JSON-RPC message and returns a response (or nil for notifications).
func (s *SSEServer) processMessage(ctx context.Context, data []byte) *Response {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return &Response{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: ErrCodeParse, Message: "parse error"},
		}
	}

	// Notifications have no ID — no response expected.
	if req.ID == nil {
		return nil
	}

	switch req.Method {
	case MethodInitialize:
		return s.handleSSEInitialize(req)
	case MethodToolsList:
		return s.handleSSEToolsList(req)
	case MethodToolsCall:
		return s.handleSSEToolsCall(ctx, req)
	case MethodPing:
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{}`)}
	default:
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: ErrCodeMethodNotFound, Message: fmt.Sprintf("unknown method: %s", req.Method)},
		}
	}
}

// handleSSEInitialize returns MCP capabilities per FR-14.4.
func (s *SSEServer) handleSSEInitialize(req Request) *Response {
	result := InitializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities: ServerCaps{
			Tools: &ToolsCap{ListChanged: false},
		},
		ServerInfo: struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{Name: "mtix", Version: s.version},
	}

	data, _ := json.Marshal(result)
	return &Response{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(data)}
}

// handleSSEToolsList returns all registered tools.
func (s *SSEServer) handleSSEToolsList(req Request) *Response {
	tools := s.tools.List()
	data, _ := json.Marshal(map[string]any{"tools": tools})
	return &Response{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(data)}
}

// handleSSEToolsCall dispatches a tool call.
func (s *SSEServer) handleSSEToolsCall(ctx context.Context, req Request) *Response {
	var params ToolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: ErrCodeInvalidParams, Message: "invalid tool call params"},
		}
	}

	result, err := s.tools.Call(ctx, params.Name, params.Arguments)
	if err != nil {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  mustMarshal(ErrorResult(err.Error())),
		}
	}

	data, _ := json.Marshal(result)
	return &Response{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(data)}
}

// BroadcastNotification sends a notification to all connected SSE clients.
func (s *SSEServer) BroadcastNotification(method string, params any) {
	data, err := json.Marshal(params)
	if err != nil {
		s.logger.Error("marshal SSE notification", "error", err)
		return
	}

	notif := Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  data,
	}

	s.mu.RLock()
	clients := make([]*sseClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	for _, client := range clients {
		if err := client.sendEvent("message", &notif); err != nil {
			s.logger.Warn("failed to send SSE notification", "error", err)
		}
	}
}

// ClientCount returns the number of connected SSE clients.
func (s *SSEServer) ClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

func (s *SSEServer) addClient(c *sseClient) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	s.logger.Info("SSE client connected", "clients", s.ClientCount())
}

func (s *SSEServer) removeClient(c *sseClient) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	close(c.done)
	s.logger.Info("SSE client disconnected", "clients", s.ClientCount())
}

// sendEvent writes an SSE event to the client.
func (c *sseClient) sendEvent(eventType string, data any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal SSE event: %w", err)
	}

	_, err = fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", eventType, payload)
	if err != nil {
		return fmt.Errorf("write SSE event: %w", err)
	}

	c.flusher.Flush()
	return nil
}

// mustMarshal marshals v or returns an error result.
func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
