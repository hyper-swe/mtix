// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

func TestPendingExports_ManyWriters_Coalesce(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir) // Parent initializes and migrates exactly once before child startup.
	pendingProcessNode(t, store, 1)
	children := make([]*coalescingWriter, 16)
	for i := range children {
		children[i] = startCoalescingWriter(t, dir)
		children[i].awaitSignal(t, "ready") // Each existing-store open completes before starting the next child.
	}
	for _, child := range children {
		_, err := io.WriteString(child.stdin, "mark\n")
		require.NoError(t, err)
	}
	for _, child := range children {
		child.awaitSignal(t, "marked 32")
		child.awaitExit(t)
	}
	t.Log("all 16 native processes completed 32 successful RequestExport calls and joined: 512 marks")
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	t.Logf("persistent request files after joined native schedule: %d", len(requests))
	require.LessOrEqual(t, len(requests), 1, "new writers must coalesce rather than persist one file per commit")
	state, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(512), state.Requested, "every actual mark advances the durable generation")
	require.Zero(t, state.Covered)
	var rows, bytes int
	require.NoError(t, store.QueryRow(context.Background(), "SELECT COUNT(*), COALESCE(MAX(LENGTH(value)),0) FROM meta WHERE key = ?", "meta.export.pending").Scan(&rows, &bytes))
	require.Equal(t, 1, rows)
	require.Less(t, bytes, 512, "logical pending record is constant size, independently of WAL size")
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir), "bounded representation must retain actual recoverable export intent")
	requests, err = pendingExportRequests(dir)
	require.NoError(t, err)
	require.Empty(t, requests)
	status, err := svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Zero(t, status.Count)
}

func TestPendingExports_ProcessWriterHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	dir, err := os.Getwd()
	require.NoError(t, err)
	path := filepath.Join(dir, "data", "mtix.db")
	_, err = os.Stat(path)
	require.NoError(t, err, "parent must initialize the database before any child opens it")
	store, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	mode := os.Args[len(os.Args)-1]
	if mode != "coalescing-writer" {
		generationProcessHelper(t, store, dir, mode)
		return
	}
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	_, err = fmt.Fprintln(os.Stdout, "ready")
	require.NoError(t, err)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if errors.Is(err, io.EOF) {
		return // Parent failure cleanup closes our owned pipe.
	}
	require.NoError(t, err)
	require.Equal(t, "mark\n", line)
	for range 32 {
		require.NoError(t, svc.RequestExport(dir))
	}
	_, err = fmt.Fprintln(os.Stdout, "marked 32")
	require.NoError(t, err)
}

type coalescingWriter struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser
	stdout io.ReadCloser
	lines  chan string
	done   chan struct{}
	err    error
	stderr bytes.Buffer
}

func startCoalescingWriter(t *testing.T, dir string) *coalescingWriter {
	return startExportRequestChild(t, dir, "coalescing-writer")
}

func startExportRequestChild(t *testing.T, dir, mode string) *coalescingWriter {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	child := &coalescingWriter{cancel: cancel, lines: make(chan string, 8), done: make(chan struct{})}
	// Fixed helper arguments; project selection is the owned child cwd, never an argv path.
	child.cmd = exec.CommandContext(ctx, binary, "-test.run=^TestPendingExports_ProcessWriterHelper$", "--", mode)
	child.cmd.Dir = dir
	child.cmd.Env = coalescingWriterEnv()
	child.cmd.Stderr = &child.stderr
	t.Cleanup(func() { child.close(t) })
	child.stdin, err = child.cmd.StdinPipe()
	require.NoError(t, err)
	child.stdout, err = child.cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.cmd.Start())
	go child.readAndWait(child.stdout)
	return child
}

func coalescingWriterEnv() []string {
	var env []string
	for _, value := range hostguard.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "MTIX_") && !strings.HasPrefix(key, "PG") {
			env = append(env, value)
		}
	}
	return env
}

func (c *coalescingWriter) readAndWait(stdout io.ReadCloser) {
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		// Assertion diagnostics can exceed the protocol buffer on early parent
		// failure. Keep draining them; only our two fixed handshake lines queue.
		if line == "ready" || line == "marked 32" || line == "snapshot-held" || line == "committed 2" || line == "writer-held" || strings.HasPrefix(line, "force-result ") {
			c.lines <- line
		}
	}
	close(c.lines)
	c.err = errors.Join(scanner.Err(), c.cmd.Wait())
	close(c.done)
}

func (c *coalescingWriter) awaitSignal(t *testing.T, want string) {
	t.Helper()
	guard := time.NewTimer(60 * time.Second)
	defer guard.Stop()
	for {
		select {
		case line, ok := <-c.lines:
			require.True(t, ok, "child exited before %q", want)
			if line == want {
				return
			}
		case <-guard.C:
			t.Fatalf("child did not signal %q", want)
		}
	}
}

func (c *coalescingWriter) awaitExit(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
		require.NoError(t, c.err, "native writer stderr: %s", c.stderr.String())
	case <-time.After(60 * time.Second):
		t.Fatal("native writer/readers did not join")
	}
}

func (c *coalescingWriter) close(t *testing.T) {
	t.Helper()
	if c.stdin != nil {
		if err := c.stdin.Close(); err != nil && !errorsIsClosedPipe(err) {
			t.Errorf("close owned writer stdin: %v", err)
		}
	}
	c.cancel() // Only the owned Cmd is terminated if the parent fails before releasing it.
	defer func() {
		if c.stdout != nil {
			if err := c.stdout.Close(); err != nil && !errorsIsClosedPipe(err) {
				t.Errorf("close owned writer stdout: %v", err)
			}
		}
	}()
	if c.cmd.Process == nil {
		return
	}
	select {
	case <-c.done:
	case <-time.After(60 * time.Second):
		t.Error("owned writer and stdout reader did not join after cancellation")
	}
}
