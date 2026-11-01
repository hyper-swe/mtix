// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// serverCallText sends one tools/call through the real Server pipeline over the
// behavior fixture's registry and returns the text the agent receives.
func serverCallText(t *testing.T, env behaviorTools, tool string, args map[string]any) (text string, isError bool) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	require.NoError(t, err)
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(string(raw)+"\n"), &out, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	srv.tools = env.reg
	require.NoError(t, srv.Serve(context.Background()))
	var resp struct {
		Result ToolsCallResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &resp), "wire: %q", out.String())
	require.Len(t, resp.Result.Content, 1)
	return resp.Result.Content[0].Text, resp.Result.IsError
}

func requirePrintableToolText(t *testing.T, text string) {
	t.Helper()
	require.True(t, utf8.ValidString(text), "tool text is valid UTF-8: %q", text)
	for _, r := range text {
		if format.SafeText(string(r)) != string(r) {
			t.Fatalf("unsafe rune %U in MCP tool text %q", r, text)
		}
	}
}

func TestMCPToolResults_HostileIDs_NoControlBytes(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 8192 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			env := newBehaviorTools(t)
			for tool, args := range map[string]map[string]any{
				"mtix_show":        {"id": f.Value},
				"mtix_dep_add":     {"from_id": f.Value, "to_id": f.Value, "dep_type": "blocks"},
				"mtix_dep_show":    {"id": f.Value},
				"mtix_claim":       {"id": f.Value, "agent_id": "agent"},
				"mtix_unclaim":     {"id": f.Value, "reason": "r"},
				"mtix_done":        {"id": f.Value},
				"mtix_prompt":      {"id": f.Value, "prompt": "p"},
				"mtix_context":     {"id": f.Value},
				"mtix_delete":      {"id": f.Value},
				"mtix_undelete":    {"id": f.Value},
				"mtix_cancel":      {"id": f.Value, "reason": f.Value},
				"mtix_reopen":      {"id": f.Value},
				"mtix_progress":    {"id": f.Value},
				"mtix_update":      {"id": f.Value, "title": "x"},
				"mtix_heartbeat":   {"agent_id": f.Value},
				"mtix_agent_state": {"agent_id": f.Value, "state": "idle"},
			} {
				text, _ := serverCallText(t, env, tool, args)
				requirePrintableToolText(t, text)
			}
		})
	}
}

func TestMCPToolResults_HostileReason_SuccessTextIsPrintable(t *testing.T) {
	for _, f := range testutil.HostileText() {
		if len(f.Value) > 8192 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			env := newBehaviorTools(t)
			node := behaviorNode(t, env, "to cancel", "")
			text, isErr := serverCallText(t, env, "mtix_cancel", map[string]any{"id": node.ID, "reason": f.Value})
			if !isErr {
				require.Contains(t, text, "Cancelled "+node.ID)
			}
			requirePrintableToolText(t, text)
		})
	}
}

func TestMCPAnnotate_HostileAddressee_SuccessTextIsEscaped(t *testing.T) {
	for _, f := range testutil.HostileText() {
		if len(f.Value) > 8192 || f.Value == "" {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			env := newBehaviorTools(t)
			node := behaviorNode(t, env, "to annotate", "")
			text, isErr := serverCallText(t, env, "mtix_annotate", map[string]any{"id": node.ID, "text": "note", "author": "author", "to": f.Value})
			requirePrintableToolText(t, text)
			if !isErr {
				require.Contains(t, text, "Annotation added to "+node.ID)
			}
		})
	}
}

func TestMCPDelete_RealNode_SuccessTextNamesTheNode(t *testing.T) {
	env := newBehaviorTools(t)
	node := behaviorNode(t, env, "to delete", "")
	text, isErr := serverCallText(t, env, "mtix_delete", map[string]any{"id": node.ID})
	require.False(t, isErr, text)
	require.Equal(t, "Deleted "+node.ID, text)
}
