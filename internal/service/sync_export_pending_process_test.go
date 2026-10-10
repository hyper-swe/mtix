// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Real-process pending-export regressions exercise Unix sync.lock contention.
// The existing Windows open-only lock fallback is outside this ticket's scope.
package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func pendingProcessStore(t *testing.T, dir string) *sqlite.Store {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0755))
	store, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), slog.Default())
	require.NoError(t, err)
	store.SetClock(pendingProcessClock)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func pendingProcessClock() time.Time { return time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC) }

func pendingProcessNode(t *testing.T, store *sqlite.Store, seq int) {
	t.Helper()
	now := pendingProcessClock()
	require.NoError(t, store.CreateNode(context.Background(), &model.Node{
		ID: fmt.Sprintf("TEST-%d", seq), Project: "TEST", Seq: seq,
		Title: fmt.Sprintf("Pending export task %d", seq), Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeEpic,
		CreatedAt: now, UpdatedAt: now,
	}))
}

func TestPendingExport_ProcessHolderRelease_CatchesUpAfterWriterExit(t *testing.T) {
	pendingProcessHolderRecovery(t, false)
}

func TestPendingExport_ProcessHolderRelease_CatchesUpAfterWriterKilled(t *testing.T) {
	pendingProcessHolderRecovery(t, true)
}

func pendingProcessHolderRecovery(t *testing.T, kill bool) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))

	holder := startPendingProcess(t, "holder", dir)
	holder.awaitLine(t, "held") // A real AutoImport is parked while holding shared sync.lock.
	mode := "writer"
	if kill {
		mode = "writerhold"
	}
	writer := startPendingProcess(t, mode, dir)
	if kill {
		writer.awaitLine(t, "committed")
		require.NoError(t, writer.cmd.Process.Kill())
		select {
		case <-writer.done:
		case <-time.After(60 * time.Second):
			t.Fatal("killed writer did not exit")
		}
	} else {
		writer.awaitExit(t)
	}
	lock, err := svc.acquireLock(dir, lockExclusive)
	require.Error(t, err, "the writer returned while the holder still owns the lock")
	require.Nil(t, lock)
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir))
	data, err := store.Export(context.Background(), "", "")
	require.NoError(t, err)
	require.Equal(t, 2, data.NodeCount, "the contended writer committed its task")

	_, err = io.WriteString(holder.stdin, "release\n")
	require.NoError(t, err)
	holder.awaitExit(t)
	require.Equal(t, 2, pendingProcessMirrorCount(t, dir),
		"releasing a real service lock holder must drain without another user write")
}

func pendingProcessMirrorCount(t *testing.T, dir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	require.NoError(t, err)
	var export sqlite.ExportData
	require.NoError(t, json.Unmarshal(data, &export))
	require.NoError(t, sqlite.ValidateExport(&export))
	return export.NodeCount
}

type pendingChild struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	lines   chan string
	done    chan struct{}
	scanned chan struct{}
	err     error
	stderr  bytes.Buffer
}

func startPendingProcess(t *testing.T, mode, dir string) *pendingChild {
	t.Helper()
	child := &pendingChild{lines: make(chan string, 16), done: make(chan struct{}), scanned: make(chan struct{})}
	child.cmd = exec.Command(os.Args[0], "-test.run=^TestPendingExportProcessHelper$", "--", mode, dir) //nolint:gosec,noctx // re-executes owned test binary; cleanup owns termination
	child.cmd.Stderr = &child.stderr
	stdin, err := child.cmd.StdinPipe()
	require.NoError(t, err)
	child.stdin = stdin
	stdout, err := child.cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.cmd.Start())
	t.Cleanup(func() { child.close(t) })
	go func() {
		defer close(child.scanned)
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			child.lines <- scan.Text()
		}
		close(child.lines)
	}()
	go func() { child.err = child.cmd.Wait(); close(child.done) }()
	return child
}

func (c *pendingChild) awaitLine(t *testing.T, want string) {
	t.Helper()
	guard := time.NewTimer(60 * time.Second)
	defer guard.Stop()
	for {
		select {
		case line, ok := <-c.lines:
			require.True(t, ok, "child exited before signal %s", want)
			if line == want {
				return
			}
		case <-guard.C:
			t.Fatalf("child did not signal %s", want)
		}
	}
}

func (c *pendingChild) awaitExit(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
		require.NoError(t, c.err, "child stderr: %s", c.stderr.String())
	case <-time.After(60 * time.Second):
		t.Fatal("child did not exit; contention must not block the writing command")
	}
}

func (c *pendingChild) close(t *testing.T) {
	t.Helper()
	if err := c.stdin.Close(); err != nil && !errorsIsClosedPipe(err) {
		t.Errorf("close child stdin: %v", err)
	}
	select {
	case <-c.done:
	case <-time.After(60 * time.Second):
		if err := c.cmd.Process.Kill(); err != nil {
			t.Errorf("kill owned child: %v", err)
		}
		<-c.done
	}
	select {
	case <-c.scanned:
	case <-time.After(60 * time.Second):
		t.Error("child stdout scanner did not join")
	}
}

func errorsIsClosedPipe(err error) bool {
	return errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}

type pendingHolderHandler struct{}

func (pendingHolderHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h pendingHolderHandler) WithAttrs([]slog.Attr) slog.Handler     { return h }
func (h pendingHolderHandler) WithGroup(string) slog.Handler          { return h }
func (pendingHolderHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "tasks.json hash unchanged, skipping auto-import" {
		return nil
	}
	if _, err := fmt.Fprintln(os.Stdout, "held"); err != nil {
		return fmt.Errorf("signal held lock: %w", err)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return fmt.Errorf("wait for lock release: %w", err)
	}
	return nil
}

func TestPendingExportProcessHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	mode, dir := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	store := pendingProcessStore(t, dir)
	logger := slog.Default()
	if mode == "holder" {
		logger = slog.New(pendingHolderHandler{})
	}
	svc := NewSyncService(store, logger, pendingProcessClock)
	switch mode {
	case "holder":
		require.NoError(t, svc.AutoImport(context.Background(), dir))
	case "writer", "writerhold":
		pendingProcessNode(t, store, 2)
		require.NoError(t, svc.AutoExport(context.Background(), dir))
		if mode == "writerhold" {
			_, err := fmt.Fprintln(os.Stdout, "committed")
			require.NoError(t, err)
			_, err = bufio.NewReader(os.Stdin).ReadString('\n')
			require.NoError(t, err)
		}
	default:
		t.Fatalf("unknown process mode %s", mode)
	}
}
