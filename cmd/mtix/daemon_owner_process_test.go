// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

type daemonOwnerEvent struct {
	Round  int    `json:"round"`
	PID    int    `json:"pid"`
	Kind   string `json:"kind"`
	Native string `json:"native,omitempty"`
}
type daemonOwnerChild struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	events  chan daemonOwnerEvent
	done    chan struct{}
	scanned chan struct{}
	err     error
	stderr  bytes.Buffer
}

func startDaemonOwnerChild(t *testing.T, dir, mode string) *daemonOwnerChild {
	t.Helper()
	c := &daemonOwnerChild{events: make(chan daemonOwnerEvent, 16), done: make(chan struct{}), scanned: make(chan struct{})}
	binary, err := os.Executable()
	require.NoError(t, err)
	c.cmd = exec.Command(binary, "-test.run=^TestDaemonOwnerProcessHelper$", "--", mode) //nolint:gosec,noctx // owned executable, cleanup terminates and joins
	c.cmd.Dir = dir                                                                      // Operator-selected cwd; no project paths from argv.
	c.cmd.Env = append(os.Environ(), transport.EnvDSN+"=")
	c.cmd.Stderr = &c.stderr
	c.stdin, err = c.cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := c.cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, c.cmd.Start())
	t.Cleanup(func() { c.close(t) })
	go c.scan(stdout)
	go func() { c.err = c.cmd.Wait(); close(c.done) }()
	return c
}
func (c *daemonOwnerChild) scan(stdout io.Reader) {
	defer close(c.scanned)
	defer close(c.events)
	scan := bufio.NewScanner(stdout)
	for scan.Scan() {
		var event daemonOwnerEvent
		if err := json.Unmarshal(scan.Bytes(), &event); err == nil {
			c.events <- event
		}
	}
	if err := scan.Err(); err != nil {
		c.events <- daemonOwnerEvent{Kind: "protocol-error", Native: err.Error()}
	}
}
func (c *daemonOwnerChild) command(t *testing.T, command string) {
	t.Helper()
	_, err := fmt.Fprintln(c.stdin, command)
	require.NoError(t, err)
}
func (c *daemonOwnerChild) event(t *testing.T, round int, kind string) daemonOwnerEvent {
	t.Helper()
	select {
	case event, ok := <-c.events:
		require.True(t, ok, "owned child exited before %s", kind)
		require.Equal(t, round, event.Round, "protocol round")
		if kind != "outcome" {
			require.Equal(t, kind, event.Kind)
		}
		require.Equal(t, c.cmd.Process.Pid, event.PID, "event must identify actual owned child")
		return event
	case <-time.After(60 * time.Second):
		t.Fatalf("owned child did not signal %s in round%d", kind, round)
	}
	return daemonOwnerEvent{}
}
func (c *daemonOwnerChild) join(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
		require.NoError(t, c.err, "child stderr: %s", c.stderr.String())
	case <-time.After(60 * time.Second):
		t.Fatal("owned child did not exit")
	}
}
func (c *daemonOwnerChild) close(t *testing.T) {
	t.Helper()
	if err := c.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Errorf("close child stdin: %v", err)
	}
	select {
	case <-c.done:
	case <-time.After(60 * time.Second):
		if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill owned child: %v", err)
		}
		select {
		case <-c.done:
		case <-time.After(60 * time.Second):
			t.Error("killed child did not join")
		}
	}
	select {
	case <-c.scanned:
	case <-time.After(60 * time.Second):
		t.Error("child scanner did not join")
	}
}
