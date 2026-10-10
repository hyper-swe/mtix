// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDaemonPendingExport_ProcessTickAfterWriterExit(t *testing.T) {
	for _, spelling := range []string{"daemon", "syncdaemon"} {
		t.Run(spelling, func(t *testing.T) { testDaemonPendingProcess(t, spelling) })
	}
}

func testDaemonPendingProcess(t *testing.T, spelling string) {
	dir := t.TempDir()
	store := newDaemonTestStore(t, dir)
	svc := service.NewSyncService(store, slog.Default(), time.Now)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	lock, err := os.OpenFile(filepath.Join(dir, "data", "sync.lock"), os.O_RDWR, 0644)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })
	require.NoError(t, unix.Flock(int(lock.Fd()), unix.LOCK_SH|unix.LOCK_NB))
	writer := startDaemonPendingChild(t, "writer", dir)
	writer.awaitExit(t)
	requests, err := os.ReadDir(filepath.Join(dir, "data", "export-pending"))
	require.NoError(t, err)
	require.Len(t, requests, 1)
	daemon := startDaemonPendingChild(t, spelling, dir)
	daemon.awaitReady(t) // Its immediate recovery pass encountered the held lock.
	require.NoError(t, unix.Flock(int(lock.Fd()), unix.LOCK_UN))
	daemon.awaitExit(t) // A real timer tick publishes, then its logger cancels the daemon.
	data, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	require.NoError(t, err)
	var board sqlite.ExportData
	require.NoError(t, json.Unmarshal(data, &board))
	require.Equal(t, 1, board.NodeCount, "daemon tick recovers an exited writer without another mutation")
	requests, err = os.ReadDir(filepath.Join(dir, "data", "export-pending"))
	require.NoError(t, err)
	require.Empty(t, requests)
}

type daemonPendingChild struct {
	cmd     *exec.Cmd
	ready   chan struct{}
	done    chan struct{}
	scanned chan struct{}
	err     error
	stderr  bytes.Buffer
}

func startDaemonPendingChild(t *testing.T, mode, dir string) *daemonPendingChild {
	t.Helper()
	child := &daemonPendingChild{ready: make(chan struct{}), done: make(chan struct{}), scanned: make(chan struct{})}
	binary, err := os.Executable()
	require.NoError(t, err)
	child.cmd = exec.Command(binary, "-test.run=^TestDaemonPendingExportProcessHelper$", "--", mode) //nolint:gosec,noctx // owned test binary, cleanup kills and joins
	child.cmd.Env = append(os.Environ(), transport.EnvDSN+"=")
	// Owned child discovers its operator-selected project via cwd.
	child.cmd.Dir = dir
	child.cmd.Stderr = &child.stderr
	stdout, err := child.cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.cmd.Start())
	go func() {
		defer close(child.scanned)
		scan := bufio.NewScanner(stdout)
		signalled := false
		for scan.Scan() {
			if scan.Text() == "attempted" && !signalled {
				close(child.ready)
				signalled = true
			}
		}
	}()
	go func() { child.err = child.cmd.Wait(); close(child.done) }()
	t.Cleanup(func() {
		select {
		case <-child.done:
			select {
			case <-child.scanned:
			case <-time.After(60 * time.Second):
				t.Error("child stdout scanner did not join")
			}
			return
		default:
		}
		if err := child.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill owned daemon child: %v", err)
		}
		select {
		case <-child.done:
		case <-time.After(60 * time.Second):
			t.Error("owned daemon child did not join")
		}
		select {
		case <-child.scanned:
		case <-time.After(60 * time.Second):
			t.Error("child stdout scanner did not join")
		}
	})
	return child
}

func (c *daemonPendingChild) awaitReady(t *testing.T) {
	t.Helper()
	select {
	case <-c.ready:
	case <-c.done:
		t.Fatalf("daemon exited before attempting recovery: %v; %s", c.err, c.stderr.String())
	case <-time.After(60 * time.Second):
		t.Fatal("daemon did not attempt pending recovery")
	}
}
func (c *daemonPendingChild) awaitExit(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
		require.NoError(t, c.err, "child stderr: %s", c.stderr.String())
	case <-time.After(60 * time.Second):
		t.Fatal("daemon/writer failed to finish")
	}
}

type daemonPendingHandler struct{ cancel context.CancelFunc }

func (daemonPendingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h daemonPendingHandler) WithAttrs([]slog.Attr) slog.Handler     { return h }
func (h daemonPendingHandler) WithGroup(string) slog.Handler          { return h }
func (h daemonPendingHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "could not acquire sync lock, skipping auto-export" {
		_, err := fmt.Fprintln(os.Stdout, "attempted")
		return err
	}
	if record.Message == "sync_export_completed" {
		h.cancel()
	}
	return nil
}

func TestDaemonPendingExportProcessHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	dir, err := os.Getwd()
	require.NoError(t, err)
	store := newDaemonTestStore(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(daemonPendingHandler{cancel: cancel})
	svc := service.NewSyncService(store, logger, time.Now)
	if mode == "writer" {
		now := time.Now().UTC()
		require.NoError(t, store.CreateNode(ctx, &model.Node{ID: "PEND-1", Project: "PEND", Seq: 1,
			Title: "Writer exits during contention", Status: model.StatusOpen, Priority: model.PriorityMedium,
			Weight: 1, NodeType: model.NodeTypeEpic, CreatedAt: now, UpdatedAt: now}))
		require.NoError(t, svc.AutoExport(ctx, dir))
		return
	}
	app = appContext{mtixDir: dir, store: store, logger: logger, syncSvc: svc}
	switch mode {
	case "daemon":
		require.NoError(t, runDaemon(ctx, os.Stdout, os.Stderr, nil, transport.Options{}, 1))
	case "syncdaemon":
		require.NoError(t, runSyncDaemon(ctx, os.Stdout, os.Stderr, nil, transport.Options{}, 1, false))
	default:
		t.Fatalf("unknown daemon child mode %s", mode)
	}
}
