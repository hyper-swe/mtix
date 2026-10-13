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
			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInboxWaitSignalProcessHelper$") //nolint:gosec // only re-executes this test binary
			child.Env = append(os.Environ(), "MTIX_TEST_INBOX_SIGNAL=1")
			pipe, err := child.StdoutPipe()
			require.NoError(t, err)
			control, err := child.StdinPipe()
			require.NoError(t, err)
			t.Cleanup(func() {
				if err := control.Close(); err != nil {
					assert.ErrorIs(t, err, os.ErrClosed)
				}
			})
			var stderr bytes.Buffer
			child.Stderr = &stderr
			require.NoError(t, child.Start())
			finished := make(chan struct{})
			scanned := make(chan struct{})
			done := make(chan error, 1)
			go func() { done <- child.Wait(); close(finished) }()
			t.Cleanup(func() {
				select {
				case <-finished:
				default:
					if err := child.Process.Kill(); err != nil {
						assert.ErrorIs(t, err, os.ErrProcessDone)
					}
					<-finished
				}
				<-scanned
			})
			ready := make(chan bool, 1)
			go func() {
				defer close(scanned)
				scanner := bufio.NewScanner(pipe)
				for scanner.Scan() {
					if scanner.Text() == "INBOX_POLL_READY" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case got := <-ready:
				require.True(t, got, "child never polled")
			case <-time.After(5 * time.Second):
				t.Fatal("child did not become ready")
			}
			// The ready marker is emitted from the first real InboxList query,
			// after production signal setup, rather than guessing startup timing.
			signalErr := child.Process.Signal(tc.signal)
			if signalErr != nil {
				assert.ErrorIs(t, signalErr, os.ErrProcessDone)
			}
			// Release the first-poll barrier after delivering the signal. A
			// missing handler kills the old child here instead of timing out.
			if _, err := control.Write([]byte{1}); err != nil {
				t.Logf("child exited before first-poll release: %v", err)
			}
			select {
			case err := <-done:
				require.Error(t, err)
				assert.Equal(t, 7, child.ProcessState.ExitCode(), "signal must exit cleanly, distinct from success and empty timeout: %s", stderr.String())
				assert.NotContains(t, stderr.String(), "panic")
			case <-time.After(5 * time.Second):
				t.Fatal("signal did not stop the waiter")
			}
		})
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
