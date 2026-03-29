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
	"sync"
)

// Server implements the MCP protocol over stdio transport per MTIX-6.1.1.
// Reads JSON-RPC 2.0 messages from stdin, dispatches to handlers,
// and writes responses to stdout. Logs go to a file (never stdout).
type Server struct {
	reader io.Reader
	writer io.Writer
	logger *slog.Logger
	tools  *ToolRegistry
	mu     sync.Mutex // Serializes writes to stdout.

	// initialized tracks whether the client has completed handshake.
	initialized bool

	// version is the server version string.
	version string
}

// NewServer creates a new MCP server with the given I/O streams.
// In stdio mode, reader=os.Stdin, writer=os.Stdout.
// Logs MUST go to a file — never stdout (per MTIX-6.1.1).
func NewServer(reader io.Reader, writer io.Writer, logger *slog.Logger, version string) *Server {
	return &Server{
		reader:  reader,
		writer:  writer,
		logger:  logger,
		tools:   NewToolRegistry(),
		version: version,
	}
}

// Registry returns the tool registry for registering tools.
func (s *Server) Registry() *ToolRegistry {
	return s.tools
}

// Serve reads and processes JSON-RPC messages until EOF or ctx cancellation.
func (s *Server) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.reader)

	// MCP uses newline-delimited JSON-RPC over stdio.
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			return nil // EOF — client disconnected.
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		if err := s.handleMessage(ctx, line); err != nil {
			s.logger.Error("handle message", "error", err)
		}
	}
}

// handleMessage parses and dispatches a single JSON-RPC message.
func (s *Server) handleMessage(ctx context.Context, data []byte) error {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return s.sendError(nil, ErrCodeParse, "parse error")
	}

	if req.JSONRPC != "2.0" {
		return s.sendError(req.ID, ErrCodeInvalidRequest, "invalid jsonrpc version")
	}

	// Notifications have no ID and expect no response.
	if req.ID == nil {
		return s.handleNotification(ctx, &req)
	}

	return s.handleRequest(ctx, &req)
}

// handleRequest dispatches a request that expects a response.
func (s *Server) handleRequest(ctx context.Context, req *Request) error {
	switch req.Method {
	case MethodInitialize:
		return s.handleInitialize(ctx, req)
	case MethodToolsList:
		return s.handleToolsList(ctx, req)
	case MethodToolsCall:
		return s.handleToolsCall(ctx, req)
	case MethodPing:
		return s.sendResult(req.ID, map[string]string{})
	default:
		return s.sendError(req.ID, ErrCodeMethodNotFound,
			fmt.Sprintf("method not found: %s", req.Method))
	}
}

// handleNotification processes notifications (no response sent).
func (s *Server) handleNotification(_ context.Context, req *Request) error {
	switch req.Method {
	case MethodInitialized:
		s.logger.Info("client initialized")
		return nil
	default:
		s.logger.Debug("unknown notification", "method", req.Method)
		return nil
	}
}

// handleInitialize processes the initialize handshake per MCP spec.
func (s *Server) handleInitialize(_ context.Context, req *Request) error {
	var params InitializeParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.sendError(req.ID, ErrCodeInvalidParams, "invalid initialize params")
		}
	}

	s.logger.Info("client connecting",
		"client", params.ClientInfo.Name,
		"version", params.ClientInfo.Version,
		"protocol", params.ProtocolVersion)

	s.initialized = true

	result := InitializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities: ServerCaps{
			Tools: &ToolsCap{ListChanged: false},
		},
		ServerInfo: ServerInfo{
			Name:    "mtix",
			Version: s.version,
		},
	}

	return s.sendResult(req.ID, result)
}

// handleToolsList returns all registered tools per MCP spec.
func (s *Server) handleToolsList(_ context.Context, req *Request) error {
	result := ToolsListResult{
		Tools: s.tools.List(),
	}
	return s.sendResult(req.ID, result)
}

// handleToolsCall dispatches a tool invocation per MCP spec.
func (s *Server) handleToolsCall(ctx context.Context, req *Request) error {
	var params ToolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.sendError(req.ID, ErrCodeInvalidParams, "invalid tools/call params")
	}

	result, err := s.tools.Call(ctx, params.Name, params.Arguments)
	if err != nil {
		return s.sendResult(req.ID, ErrorResult(err.Error()))
	}

	return s.sendResult(req.ID, result)
}

// sendResult writes a successful JSON-RPC response.
func (s *Server) sendResult(id json.RawMessage, result any) error {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	return s.writeJSON(resp)
}

// sendError writes an error JSON-RPC response.
func (s *Server) sendError(id json.RawMessage, code int, msg string) error {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	}
	return s.writeJSON(resp)
}

// SendNotification sends a JSON-RPC notification to the client.
func (s *Server) SendNotification(method string, params any) error {
	notif := Notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return s.writeJSON(notif)
}

// writeJSON serializes and writes a JSON message followed by newline.
func (s *Server) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.writer.Write(data); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	if _, err := s.writer.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write newline: %w", err)
	}
	return nil
}
