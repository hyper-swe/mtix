//go:build linux || darwin

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Owned POSIX executables and process groups exercise installation and child cleanup.
package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestMergeBaseBoard_ExecutableTrust_ValidatesLocationModeAndOwner(t *testing.T) {
	script := boardScript(t, "exit 0")
	root, err := filepath.EvalSymlinks(filepath.Dir(script))
	require.NoError(t, err)
	require.NotEmpty(t, boardTrustedScript(t, script))
	for _, mode := range []os.FileMode{0600, 0722} {
		require.NoError(t, os.Chmod(script, mode))
		_, err = trustedBoardGit(script, root)
		require.ErrorIs(t, err, ErrNoMergeBase)
	}
	require.NoError(t, os.Chmod(script, 0700))
	require.NoError(t, os.Chmod(root, 0777))
	_, err = trustedBoardGit(script, root)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.NoError(t, os.Chmod(root, 0700))
	for _, path := range []string{"relative", filepath.Join(root, "absent"), root} {
		_, err = trustedBoardGit(path, root)
		require.ErrorIs(t, err, ErrNoMergeBase)
	}
	_, err = trustedBoardGit(script, "/")
	require.ErrorIs(t, err, ErrNoMergeBase)
	outside := boardScript(t, "exit 0")
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(outside, link))
	_, err = trustedBoardGit(link, root)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.False(t, boardInstallation("/usr/locality/git"))
	require.True(t, boardInstallation("/usr/bin/git"))
}

func TestMergeBaseBoard_Cancellation_ContainsOwnedDescendant(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	block := filepath.Join(root, "block")
	require.NoError(t, syscall.Mkfifo(ready, 0600))
	require.NoError(t, syscall.Mkfifo(block, 0600))
	child := boardScript(t, "printf '%s\\n' \"$$\" > '"+ready+"'; read value < '"+block+"'")
	script := boardScript(t, "/bin/sh '"+child+"' & wait")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := boardTrustedScript(t, script)
	done := make(chan error, 1)
	go func() {
		_, err := readGit(ctx, path, root, 1024, "show")
		done <- err
	}()
	pid := boardReadyPID(t, ready, cancel)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("owned process did not join")
	}
	deadline, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	for syscall.Kill(pid, 0) == nil {
		select {
		case <-deadline.Done():
			t.Fatal("owned descendant did not exit")
		default:
			runtime.Gosched()
		}
	}
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH)
}

func boardReadyPID(t *testing.T, path string, cancel context.CancelFunc) int {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	pid := make(chan int, 1)
	fail := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(file)
		if !scanner.Scan() {
			fail <- fmt.Errorf("owned process readiness missing")
			return
		}
		value, parseErr := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if parseErr != nil {
			fail <- parseErr
			return
		}
		pid <- value
	}()
	select {
	case value := <-pid:
		return value
	case readErr := <-fail:
		cancel()
		t.Fatal(readErr)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("owned process readiness unavailable")
	}
	return 0
}

func TestMergeBaseBoard_TrustMetadata_RequiresExistingDirectory(t *testing.T) {
	root := t.TempDir()
	err := boardOwnedMode(filepath.Join(root, "absent"), false)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.Contains(t, err.Error(), "metadata unavailable")
	file := filepath.Join(root, "regular")
	boardWrite(t, file, []byte("owned executable"))
	require.NoError(t, os.Chmod(file, 0700))
	require.NoError(t, boardOwnedMode(file, false))
	err = boardOwnedMode(file, true)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.Contains(t, err.Error(), "untrusted Git directory")
	require.NoError(t, boardOwnedMode(root, true))
}

func TestMergeBaseBoard_Success_ContainsDisconnectedOwnedDescendant(t *testing.T) {
	root := t.TempDir()
	ready, ack, block := filepath.Join(root, "ready"), filepath.Join(root, "ack"), filepath.Join(root, "block")
	for _, path := range []string{ready, ack, block} {
		require.NoError(t, syscall.Mkfifo(path, 0600))
	}
	child := boardScript(t, "printf '%s\\n' \"$$\" > '"+ready+"'; printf 'ready\\n' > '"+ack+"'; read value < '"+block+"'")
	script := boardScript(t, "/bin/sh '"+child+"' </dev/null >/dev/null 2>&1 & read value < '"+ack+"'; printf 'completed\\n'")
	path := boardTrustedScript(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := readGit(ctx, path, root, 1024, "show")
		done <- result{data, err}
	}()
	pid := boardReadyPID(t, ready, cancel)
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Equal(t, "completed\n", string(got.data))
	case <-ctx.Done():
		t.Fatal("successful owned process did not join")
	}
	require.NoError(t, ctx.Err())
	boardRequireExited(t, pid)
	exited = true
}

func boardRequireExited(t *testing.T, pid int) {
	t.Helper()
	deadline, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	for syscall.Kill(pid, 0) == nil {
		select {
		case <-deadline.Done():
			t.Fatal("owned descendant did not exit")
		default:
			runtime.Gosched()
		}
	}
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH)
}

// Each stage announces its owned process before the test releases its response.
type boardExchange struct {
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan boardExchangeResult
	ready           chan string
	readerDone      chan struct{}
	reader, release *os.File
	pid             atomic.Int64
	joined          bool
}

type boardExchangeResult struct {
	data *sqlite.ExportData
	err  error
}

func newBoardExchange(t *testing.T, root string, body func(string, string) string) *boardExchange {
	t.Helper()
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	for _, p := range []string{ready, release} {
		require.NoError(t, syscall.Mkfifo(p, 0600))
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	op := &boardExchange{ctx: ctx, cancel: cancel, done: make(chan boardExchangeResult, 1), ready: make(chan string, 2), readerDone: make(chan struct{})}
	reader, err := os.OpenFile(ready, os.O_RDWR, 0)
	require.NoError(t, err)
	op.reader = reader
	t.Cleanup(func() { _ = reader.Close() })
	writer, err := os.OpenFile(release, os.O_RDWR, 0)
	if err != nil {
		cancel()
		_ = reader.Close()
	}
	require.NoError(t, err)
	op.release = writer
	t.Cleanup(func() { _ = writer.Close() })
	path := boardTrustedScript(t, boardScript(t, body(ready, release)))
	t.Cleanup(func() { op.cleanup(t) })
	go op.observe()
	go func() {
		data, _, err := readMergeBaseBoard(ctx, root, path, DefaultMaxImportSize)
		op.done <- boardExchangeResult{data, err}
	}()
	return op
}

func (op *boardExchange) observe() {
	defer close(op.readerDone)
	scanner := bufio.NewScanner(op.reader)
	for scanner.Scan() {
		event := scanner.Text()
		if event == "STOP" {
			return
		}
		if pid, err := strconv.Atoi(event); err == nil {
			op.pid.Store(int64(pid))
		}
		select {
		case op.ready <- event:
		case <-op.ctx.Done():
			return
		}
	}
}

func (op *boardExchange) cleanup(t *testing.T) {
	op.cancel()
	if pid := int(op.pid.Load()); pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	_ = op.release.Close()
	_, _ = op.reader.WriteString("STOP\n")
	<-op.readerDone
	_ = op.reader.Close()
	if !op.joined {
		select {
		case <-op.done:
			op.joined = true
		case <-time.After(3 * time.Second):
			t.Error("owned exchange did not join after cancellation")
		}
	}
}

func boardTestWatchdog(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		return context.WithDeadline(context.Background(), deadline.Add(-3*time.Second))
	}
	return context.WithCancel(context.Background())
}

func (op *boardExchange) next(t *testing.T) (int, error) {
	t.Helper()
	watchdog, stop := boardTestWatchdog(t)
	defer stop()
	select {
	case event := <-op.ready:
		pid, err := strconv.Atoi(event)
		if err != nil || pid <= 0 {
			return 0, errors.New("invalid owned stage event")
		}
		return pid, nil
	case got := <-op.done:
		op.joined = true
		return 0, fmt.Errorf("owned stage exited before readiness: %w", got.err)
	case <-watchdog.Done():
		return 0, watchdog.Err()
	}
}

func (op *boardExchange) finish(t *testing.T) boardExchangeResult {
	t.Helper()
	watchdog, stop := boardTestWatchdog(t)
	defer stop()
	select {
	case got := <-op.done:
		op.joined = true
		return got
	case <-watchdog.Done():
		t.Fatal("owned exchange completion unavailable")
	}
	return boardExchangeResult{}
}

func TestMergeBaseBoard_ExactlyTwoOperationsAndClosedInput(t *testing.T) {
	root, blob, oid := boardPullFixture(t)
	before := boardSnapshot(t, root)
	log, raw := filepath.Join(t.TempDir(), "calls"), filepath.Join(t.TempDir(), "blob")
	boardWrite(t, raw, blob)
	op := newBoardExchange(t, root, func(ready, release string) string {
		return "printf '%s\\n' \"$@\" >> '" + log + "'; printf 'CALL-END\\n' >> '" + log + "'; if read value; then exit 9; fi; printf '%s\\n' \"$$\" > '" + ready + "'; read value < '" + release + "'; for arg; do if [ \"$arg\" = merge-base ]; then printf '%s\\n' '" + oid + "'; exit 0; fi; done; /bin/cat '" + raw + "'"
	})
	for range 2 {
		_, err := op.next(t)
		require.NoError(t, err)
		_, err = op.release.WriteString("release\n")
		require.NoError(t, err)
	}
	got := op.finish(t)
	require.NoError(t, got.err)
	require.Equal(t, "ancestor board", got.data.Nodes[0].Title)
	recorded, err := os.ReadFile(log)
	require.NoError(t, err)
	calls := strings.Split(strings.TrimSuffix(string(recorded), "CALL-END\n"), "CALL-END\n")
	require.Len(t, calls, 2)
	policy := []string{"--no-pager", "--no-replace-objects", "--no-lazy-fetch", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "protocol.allow=never", "-c", "credential.helper="}
	expected := [][]string{append(append([]string{}, policy...), "merge-base", "--all", "ORIG_HEAD", "@{upstream}"), append(append([]string{}, policy...), "show", "--no-ext-diff", "--no-textconv", oid+":.mtix/tasks.json")}
	for i, call := range calls {
		require.Equal(t, strings.Join(expected[i], "\n")+"\n", call)
	}
	require.Equal(t, before, boardSnapshot(t, root))
}

func TestMergeBaseBoard_TwoOperations_ShareCallerBudget(t *testing.T) {
	root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
	before := boardSnapshot(t, root)
	op := newBoardExchange(t, root, func(ready, release string) string {
		return "for arg; do if [ \"$arg\" = merge-base ]; then printf '%s\\n' '" + strings.Repeat("a", 40) + "'; exit 0; fi; done; printf '%s\\n' \"$$\" > '" + ready + "'; read value < '" + release + "'"
	})
	pid, err := op.next(t)
	require.NoError(t, err)
	op.cancel()
	select {
	case got := <-op.done:
		op.joined = true
		require.ErrorIs(t, got.err, ErrNoMergeBase)
		require.ErrorIs(t, got.err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("second operation ignored caller cancellation")
	}
	boardRequireExited(t, pid)
	require.Equal(t, before, boardSnapshot(t, root))
}

func TestMergeBaseBoard_ExpiredCallerDeadline_DoesNotExecute(t *testing.T) {
	root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
	before := boardSnapshot(t, root)
	log := filepath.Join(t.TempDir(), "calls")
	path := boardTrustedScript(t, boardScript(t, "printf 'called' > '"+log+"'"))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, _, err := readMergeBaseBoard(ctx, root, path, DefaultMaxImportSize)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = os.Stat(log)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Equal(t, before, boardSnapshot(t, root))
	require.Equal(t, 5*time.Second, gitBoardTimeout)
}

func TestMergeBaseBoard_StageReadiness_HandlesEarlyExitAndInvalidEvent(t *testing.T) {
	for _, body := range []string{"exit 7", "printf 'invalid\\n' > '%s'; exit 8"} {
		t.Run(body, func(t *testing.T) {
			root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
			op := newBoardExchange(t, root, func(ready, _ string) string {
				if strings.Contains(body, "%s") {
					return fmt.Sprintf(body, ready)
				}
				return body
			})
			_, err := op.next(t)
			require.Error(t, err)
			op.cleanup(t)
			require.True(t, op.joined)
		})
	}
}
