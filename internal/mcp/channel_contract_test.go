// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-56.7 pinned contract test: the exact JSON-RPC frames the Claude Code
// channels research preview expects (code.claude.com/docs/en/channels-reference).
// If Anthropic revises the contract, THIS test is the early warning, and
// internal/channel/claudecode.go is the whole blast radius.
package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/channel"
	"github.com/hyper-swe/mtix/internal/mcp"
)

// TestChannelContract_InitializeDeclaresCapability: the initialize result must
// carry capabilities.experimental["claude/channel"] = {} (this is what makes
// Claude Code register a notification listener) plus the instructions string.
func TestChannelContract_InitializeDeclaresCapability(t *testing.T) {
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"claude-code","version":"2.1.80"}}}` + "\n")
	var out bytes.Buffer
	srv := mcp.NewServer(in, &out, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	srv.DeclareExperimental(channel.ClaudeCapabilityKey, channel.ClaudeInstructions("worker"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, srv.Serve(ctx))

	var resp struct {
		Result struct {
			Capabilities struct {
				Experimental map[string]any `json:"experimental"`
			} `json:"capabilities"`
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &resp))
	require.Contains(t, resp.Result.Capabilities.Experimental, "claude/channel",
		"the experimental capability key registers the channel listener")
	assert.Equal(t, map[string]any{}, resp.Result.Capabilities.Experimental["claude/channel"],
		"the capability value is the empty object")
	assert.Contains(t, resp.Result.Instructions, `agent "worker"`)
	assert.Contains(t, resp.Result.Instructions, "mtix_inbox_ack",
		"instructions teach the ack half of the loop")
}

// TestChannelContract_NotificationFrame: one pushed event must serialize to
// exactly the notification method and params shape the preview documents —
// {content, meta} with identifier-only meta keys.
func TestChannelContract_NotificationFrame(t *testing.T) {
	var out bytes.Buffer
	srv := mcp.NewServer(strings.NewReader(""), &out, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")

	ad := channel.NewClaudeCode(srv.SendNotification)
	require.NoError(t, ad.Push(channel.Event{
		Seq: 41, Node: "PROJ-7", From: "planner",
		Body: "Start on PROJ-7. Plan context attached.",
	}))

	var frame map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out.Bytes(), &frame))
	assert.JSONEq(t, `"2.0"`, string(frame["jsonrpc"]))
	assert.JSONEq(t, `"notifications/claude/channel"`, string(frame["method"]))
	assert.JSONEq(t, `{
		"content": "Start on PROJ-7. Plan context attached.",
		"meta": {"from": "planner", "node": "PROJ-7", "seq": "41"}
	}`, string(frame["params"]))
	_, hasID := frame["id"]
	assert.False(t, hasID, "a notification carries no id")

	for key := range map[string]string{"from": "", "node": "", "seq": ""} {
		assert.Regexp(t, `^[A-Za-z0-9_]+$`, key,
			"meta keys must be bare identifiers — the client silently drops others")
	}
}
