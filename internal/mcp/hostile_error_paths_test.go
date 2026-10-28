// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func hostileFailTool(reg *ToolRegistry, name string) {
	reg.Register(ToolDef{Name: name, Description: "fails", InputSchema: SchemaObj{Type: "object"}},
		func(_ context.Context, args json.RawMessage) (*ToolsCallResult, error) {
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			return nil, errors.New("node " + p.Text + ": not found")
		})
}

func rpcLine(t *testing.T, id any, method string, params any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	require.NoError(t, err)
	return string(raw) + "\n"
}

// TestInboxWaitWorkerError_HostileText_IsEscaped registers the failing tool as a
// cancellable wait, so Serve dispatches it to runInboxWait (the worker path),
// not to handleToolsCall.
func TestInboxWaitWorkerError_HostileText_IsEscaped(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 8192 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			in := rpcLine(t, 7, MethodToolsCall, map[string]any{"name": "mtix_inbox_wait", "arguments": map[string]string{"text": f.Value}})
			pr, pw := io.Pipe()
			out := &syncBuffer{}
			srv := NewServer(pr, out, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
			hostileFailTool(srv.tools, "mtix_inbox_wait")
			srv.tools.mu.Lock()
			wait := srv.tools.tools["mtix_inbox_wait"]
			wait.def.CancellableWait = true
			srv.tools.tools["mtix_inbox_wait"] = wait
			srv.tools.mu.Unlock()
			require.True(t, srv.tools.isCancellableWait("mtix_inbox_wait"))

			done := make(chan error, 1)
			go func() { done <- srv.Serve(context.Background()) }()
			_, err := pw.Write([]byte(in))
			require.NoError(t, err)
			// Keep stdin open until the worker answers: EOF would cancel the wait.
			require.Eventually(t, func() bool { return strings.Contains(out.String(), "\n") }, 5*time.Second, 5*time.Millisecond)
			require.NoError(t, pw.Close())
			require.NoError(t, <-done)

			var resp struct {
				Result ToolsCallResult `json:"result"`
			}
			require.NoError(t, json.Unmarshal([]byte(out.String()), &resp), "wire %q", out.String())
			require.True(t, resp.Result.IsError)
			require.Len(t, resp.Result.Content, 1)
			requirePrintableToolText(t, resp.Result.Content[0].Text)
		})
	}
}

// syncBuffer is a bytes.Buffer safe for the worker goroutine to write while the
// test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSSEToolError_HostileText_IsEscaped(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 8192 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			reg := NewToolRegistry()
			hostileFailTool(reg, "mtix_fail")
			srv := NewSSEServer(reg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
			msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": MethodToolsCall,
				"params": map[string]any{"name": "mtix_fail", "arguments": map[string]string{"text": f.Value}}})
			require.NoError(t, err)
			resp := srv.processMessage(context.Background(), msg)
			require.NotNil(t, resp)
			var result ToolsCallResult
			raw, err := json.Marshal(resp.Result)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &result))
			require.True(t, result.IsError)
			requirePrintableToolText(t, result.Content[0].Text)
		})
	}
}

func TestMethodNotFound_HostileMethod_IsEscapedOnBothTransports(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 8192 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			var out bytes.Buffer
			srv := NewServer(strings.NewReader(rpcLine(t, 1, f.Value, map[string]any{})), &out, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
			require.NoError(t, srv.Serve(context.Background()))
			var resp Response
			require.NoError(t, json.Unmarshal(out.Bytes(), &resp), "wire %q", out.String())
			require.NotNil(t, resp.Error)
			requirePrintableToolText(t, resp.Error.Message)

			sse := NewSSEServer(NewToolRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
			msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": f.Value})
			require.NoError(t, err)
			sresp := sse.processMessage(context.Background(), msg)
			require.NotNil(t, sresp)
			require.NotNil(t, sresp.Error)
			requirePrintableToolText(t, sresp.Error.Message)
		})
	}
}

func TestSuccessText_HostileValues_AreEscapedAtEverySite(t *testing.T) {
	sites := map[string]func(h string) string{
		"prompt":        textPromptUpdated,
		"annotate":      func(h string) string { return textAnnotationAdded(h, h) },
		"annotate-none": func(h string) string { return textAnnotationAdded(h, "") },
		"resolved":      func(h string) string { return textAnnotationResolved(format.EventID(h), h) },
		"dep-add":       func(h string) string { return textDepAdded(h, h, h) },
		"dep-remove":    func(h string) string { return textDepRemoved(h, h, h) },
		"deleted":       textDeleted,
		"undeleted":     textUndeleted,
		"updated":       textUpdated,
		"inbox-ack":     func(h string) string { return textInboxAcked(3, h) },
		"session-end":   textSessionEnded,
		"heartbeat":     textHeartbeat,
		"agent-state":   func(h string) string { return textAgentState(h, h) },
		"agent-no-work": textAgentNoWork,
		"claimed":       func(h string) string { return textClaimed(h, h) },
		"unclaimed":     textUnclaimed,
		"done":          textDone,
		"reopened":      textReopened,
		"deferred":      func(h string) string { return textDeferred(h, "") },
		"cancelled":     func(h string) string { return textCancelled(h, h) },
		"rerun":         func(h string) string { return textRerun(h, h) },
	}
	for name, site := range sites {
		for _, f := range testutil.HostileIDs() {
			if f.Value == "" || len(f.Value) > 8192 {
				continue
			}
			text := site(f.Value)
			requirePrintableToolText(t, text)
			if name != "resolved" && strings.ContainsAny(f.Value, "\x00\x1b\n") {
				assert.NotContains(t, text, f.Value, "%s/%s echoes the raw value", name, f.Name)
			}
		}
	}
}
