// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/sync/workflow"
)

var hostileEventIDs = []string{
	"evt\x1b[31mred\x1b[0m",
	"evt\nINJECTED: line",
	"evt\x00nul",
	"evt-\u202egnp.exe",
	"evt-\u00e9\u4e2d",
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// toolErrorText runs one tools/call whose handler fails with err, and
// returns the error text the agent receives plus the raw wire line.
func toolErrorText(t *testing.T, err error) (text, wire string) {
	t.Helper()
	input := makeRequest(t, 1, "tools/call", mcp.ToolsCallParams{Name: "mtix_fail"})
	var out bytes.Buffer
	srv := mcp.NewServer(strings.NewReader(input), &out,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), "test")
	srv.Registry().Register(mcp.ToolDef{
		Name: "mtix_fail", Description: "fails", InputSchema: mcp.SchemaObj{Type: "object"},
	}, func(context.Context, json.RawMessage) (*mcp.ToolsCallResult, error) { return nil, err })
	require.NoError(t, srv.Serve(context.Background()))

	resp := parseResponse(t, out.String())
	var result mcp.ToolsCallResult
	data, merr := json.Marshal(resp.Result)
	require.NoError(t, merr)
	require.NoError(t, json.Unmarshal(data, &result))
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	return result.Content[0].Text, out.String()
}

// TestToolsCall_HostileEventIDError_TextIsPrintableASCII checks that a tool
// error carrying a rendered event id reaches the agent as printable ASCII
// both on the wire and after JSON decoding.
func TestToolsCall_HostileEventIDError_TextIsPrintableASCII(t *testing.T) {
	for _, id := range hostileEventIDs {
		t.Run(strconv.QuoteToASCII(id), func(t *testing.T) {
			text, wire := toolErrorText(t, fmt.Errorf("event %s rejected", format.EventID(id)))
			assert.True(t, isPrintableASCII(text), "text %q", text)
			assert.True(t, isPrintableASCII(strings.TrimSpace(wire)), "wire %q", wire)
			assert.Contains(t, text, format.EventID(id))
		})
	}
}

// TestToolsCall_RawEventIDError_IsDetected pins the oracle for the mutation check.
func TestToolsCall_RawEventIDError_IsDetected(t *testing.T) {
	for _, id := range hostileEventIDs {
		text, _ := toolErrorText(t, errors.New("event "+id+" rejected"))
		assert.False(t, isPrintableASCII(text), "%q", id)
	}
}

type failingSyncWorkflow struct{ err error }

func (f failingSyncWorkflow) DetectState(context.Context, string) (workflow.Report, error) {
	return workflow.Report{}, f.err
}

// TestSyncWorkflowTool_DetectStateError_NeverEchoesErrorText confirms the
// sync tool error path maps to fixed categories, so an event id in the
// underlying error cannot appear in the result.
func TestSyncWorkflowTool_DetectStateError_NeverEchoesErrorText(t *testing.T) {
	for _, id := range hostileEventIDs {
		reg := mcp.NewToolRegistry()
		mcp.RegisterSyncWorkflowTool(reg,
			failingSyncWorkflow{err: errors.New("event " + id + " broke")}, t.TempDir())
		res, err := reg.Call(context.Background(), "mtix_sync_workflow", nil)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.True(t, isPrintableASCII(res.Content[0].Text), "%q", res.Content[0].Text)
		assert.NotContains(t, res.Content[0].Text, "evt")
	}
}
