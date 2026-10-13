// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type inboxMCPChild struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	frames  chan []byte
	scanned chan struct{}
	scanErr chan error
	done    chan error
	cancel  context.CancelFunc
	joined  bool
}

func newInboxMCPChild(t *testing.T) *inboxMCPChild {
	t.Helper()
	initTestApp(t)
	project := filepath.Dir(app.mtixDir)
	require.NoError(t, app.store.Close())
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "config.yaml"), []byte("prefix: TEST\nsync:\n  auto_sync: false\n"), 0o600))
	binary, err := os.Executable()
	require.NoError(t, err)
	args, err := json.Marshal([]string{"-C", project, "mcp"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	child := &inboxMCPChild{frames: make(chan []byte, 32), scanned: make(chan struct{}), scanErr: make(chan error, 1), done: make(chan error, 1), cancel: cancel}
	child.cmd = exec.CommandContext(ctx, binary, "-test.run=^TestMCPStdio_ChildMain$") //nolint:gosec // owned current test binary runs stock CLI main
	child.cmd.Dir, child.cmd.Env = project, stdioChildEnvironment(string(args))
	child.cmd.Stderr = stdioOwnedFile(t, filepath.Join(t.TempDir(), "stderr.txt"))
	child.stdin, err = child.cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := child.cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.cmd.Start())
	t.Cleanup(func() { child.cleanup(t) })
	go child.scan(stdout)
	go func() {
		// Drain stdout before Wait closes the owned pipe.
		<-child.scanned
		child.done <- child.cmd.Wait()
	}()
	return child
}

func (child *inboxMCPChild) scan(stdout io.Reader) {
	defer close(child.scanned)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		child.frames <- append([]byte(nil), scanner.Bytes()...)
	}
	child.scanErr <- scanner.Err()
}

func (child *inboxMCPChild) cleanup(t *testing.T) {
	t.Helper()
	child.cancel()
	if err := child.stdin.Close(); err != nil {
		assert.ErrorIs(t, err, os.ErrClosed)
	}
	if !child.joined {
		select {
		case err := <-child.done:
			child.joined = true
			if err != nil {
				var exitError *exec.ExitError
				assert.ErrorAs(t, err, &exitError, "watchdog cleanup may terminate only the owned child")
				t.Logf("owned MCP watchdog cleanup: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("owned MCP subprocess Wait did not join after watchdog cancellation")
		}
	}
	select {
	case <-child.scanned:
		assert.NoError(t, <-child.scanErr, "owned stdout scanner must finish cleanly")
	case <-time.After(time.Second):
		t.Error("owned stdout scanner did not join")
	}
}

func (child *inboxMCPChild) send(t *testing.T, id any, method string, params any) {
	t.Helper()
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		message["id"] = id
	}
	if params != nil {
		message["params"] = params
	}
	data, err := json.Marshal(message)
	require.NoError(t, err)
	_, err = child.stdin.Write(append(data, '\n'))
	require.NoError(t, err)
}

func (child *inboxMCPChild) response(t *testing.T, id int) map[string]json.RawMessage {
	t.Helper()
	select {
	case frame := <-child.frames:
		var response map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(frame, &response), "stdio must contain complete JSON-RPC frames")
		want, err := json.Marshal(id)
		require.NoError(t, err)
		require.JSONEq(t, string(want), string(response["id"]))
		require.NotContains(t, response, "error")
		return response
	case <-time.After(2 * time.Second):
		t.Fatalf("stock CLI did not answer request %d", id)
		return nil
	}
}

func (child *inboxMCPChild) finish(t *testing.T) {
	t.Helper()
	require.NoError(t, child.stdin.Close())
	select {
	case err := <-child.done:
		child.joined = true
		require.NoError(t, err, "stdin EOF must stop the stock CLI normally")
	case <-time.After(2 * time.Second):
		t.Fatal("stdin EOF did not stop the owned MCP child")
	}
}

func TestInboxWaitMCPStdio_ZeroCancellationPingAndEOF(t *testing.T) {
	child := newInboxMCPChild(t)
	child.send(t, 1, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "inbox-wait-regression"}})
	stdioCheckInitialize(t, child.response(t, 1))
	child.send(t, 2, "tools/call", map[string]any{"name": "mtix_inbox_wait", "arguments": map[string]any{"agent": "worker", "timeout_seconds": 0}})
	child.send(t, nil, "notifications/cancelled", map[string]any{"requestId": 2, "reason": "wire cancellation before delivery"})
	child.send(t, 3, "ping", nil)
	child.response(t, 3)
	child.send(t, 4, "tools/call", map[string]any{"name": "mtix_inbox", "arguments": map[string]any{"agent": "worker"}})
	response := child.response(t, 4)
	require.JSONEq(t, "null", stdioContent(t, stdioResult(t, response)), "ordinary inbox preserves its existing empty-list shape")
	child.finish(t)
	assert.Empty(t, child.frames, "cancelled wait must not emit a later response")
}

func TestInboxWaitMCPStdio_OrdinaryInboxEmptyExistingShape(t *testing.T) {
	child := newInboxMCPChild(t)
	child.send(t, 1, "initialize", map[string]any{"protocolVersion": "2024-11-05"})
	stdioCheckInitialize(t, child.response(t, 1))
	child.send(t, 2, "tools/call", map[string]any{"name": "mtix_inbox", "arguments": map[string]any{"agent": "worker"}})
	response := child.response(t, 2)
	require.JSONEq(t, "null", stdioContent(t, stdioResult(t, response)))
	child.finish(t)
}

func TestInboxWaitMCPStdio_ActiveWaitEOF(t *testing.T) {
	child := newInboxMCPChild(t)
	child.send(t, 1, "initialize", map[string]any{"protocolVersion": "2024-11-05"})
	stdioCheckInitialize(t, child.response(t, 1))
	child.send(t, 2, "tools/call", map[string]any{"name": "mtix_inbox_wait", "arguments": map[string]any{"agent": "worker", "timeout_seconds": 0}})
	child.finish(t)
}
