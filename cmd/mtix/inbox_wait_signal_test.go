// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Subprocess checks send real signals only after the command has queried its inbox.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInboxWait_TimeoutZero_SignalExitsDocumentedCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal os.Signal
	}{
		{"SIGINT", os.Interrupt}, {"SIGTERM", syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := newInboxSignalChild(t)
			signalInboxChildAfterReady(t, child, tc.signal)
			select {
			case err := <-child.done:
				require.Error(t, err)
				assert.Equal(t, 7, child.cmd.ProcessState.ExitCode(), "signal must exit cleanly, distinct from success and empty timeout: %s", child.stderr.String())
				assert.NotContains(t, child.stderr.String(), "panic")
			case <-time.After(5 * time.Second):
				t.Fatal("signal did not stop the waiter")
			}
		})
	}
}

type inboxSignalChild struct {
	cmd      *exec.Cmd
	control  io.WriteCloser
	stderr   bytes.Buffer
	finished chan struct{}
	scanned  chan struct{}
	done     chan error
	ready    chan bool
}

func newInboxSignalChild(t *testing.T) *inboxSignalChild {
	t.Helper()
	child := &inboxSignalChild{finished: make(chan struct{}), scanned: make(chan struct{}), done: make(chan error, 1), ready: make(chan bool, 1)}
	child.cmd = exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInboxWaitSignalProcessHelper$") //nolint:gosec // only re-executes this test binary
	child.cmd.Env = append(os.Environ(), "MTIX_TEST_INBOX_SIGNAL=1")
	pipe, err := child.cmd.StdoutPipe()
	require.NoError(t, err)
	child.control, err = child.cmd.StdinPipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := child.control.Close(); err != nil {
			assert.ErrorIs(t, err, os.ErrClosed)
		}
	})
	child.cmd.Stderr = &child.stderr
	require.NoError(t, child.cmd.Start())
	go func() { child.done <- child.cmd.Wait(); close(child.finished) }()
	t.Cleanup(func() { child.cleanup(t) })
	go child.scanReady(pipe)
	return child
}

func (child *inboxSignalChild) cleanup(t *testing.T) {
	t.Helper()
	select {
	case <-child.finished:
	default:
		if err := child.cmd.Process.Kill(); err != nil {
			assert.ErrorIs(t, err, os.ErrProcessDone)
		}
		select {
		case <-child.finished:
		case <-time.After(3 * time.Second):
			t.Error("owned signal child Wait did not join")
		}
	}
	select {
	case <-child.scanned:
	case <-time.After(time.Second):
		t.Error("owned signal child scanner did not join")
	}
}

func (child *inboxSignalChild) scanReady(pipe io.Reader) {
	defer close(child.scanned)
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		if scanner.Text() == "INBOX_POLL_READY" {
			child.ready <- true
			return
		}
	}
	child.ready <- false
}

func signalInboxChildAfterReady(t *testing.T, child *inboxSignalChild, signal os.Signal) {
	t.Helper()
	select {
	case got := <-child.ready:
		require.True(t, got, "child never polled")
	case <-time.After(5 * time.Second):
		t.Fatal("child did not become ready")
	}
	// Marker is from the first real query, after production signal setup.
	if err := child.cmd.Process.Signal(signal); err != nil {
		assert.ErrorIs(t, err, os.ErrProcessDone)
	}
	// Release the first-poll barrier after signal delivery. Missing handlers
	// kill the old child here instead of timing out.
	if _, err := child.control.Write([]byte{1}); err != nil {
		t.Logf("child exited before first-poll release: %v", err)
	}
}

func TestInboxWaitSignalProcessHelper(t *testing.T) {
	if os.Getenv("MTIX_TEST_INBOX_SIGNAL") != "1" {
		return
	}
	code := 1
	t.Cleanup(func() { os.Exit(code) })
	initRegistrationApp(t)
	t.Setenv("CODEX_THREAD_ID", "inbox-signal-session")
	var once sync.Once
	app.store.SetClock(func() time.Time {
		once.Do(func() {
			fmt.Fprintln(os.Stdout, "INBOX_POLL_READY")
			var release [1]byte
			_, err := io.ReadFull(os.Stdin, release[:])
			require.NoError(t, err)
		})
		return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	})
	cmd := newInboxCmd()
	cmd.SetArgs(strings.Fields("--agent worker --wait --timeout 0 --format context"))
	err := cmd.Execute()
	if closeErr := closeApp(); closeErr != nil {
		fmt.Fprintln(os.Stderr, closeErr)
		os.Exit(1)
	}
	// Run the helper's cleanup before exit because os.Exit skips deferred cleanup.
	code = exitCodeForError(err)
}
