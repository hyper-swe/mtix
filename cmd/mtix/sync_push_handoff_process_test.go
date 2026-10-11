// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/stretchr/testify/require"
)

type pushHandoffChild struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	frames  chan pushHandoffFrame
	done    chan struct{}
	err     error
	scanErr error
	stderr  bytes.Buffer
}

func startPushHandoffChild(t *testing.T, dir, mode string) *pushHandoffChild {
	t.Helper()
	c := &pushHandoffChild{frames: make(chan pushHandoffFrame, 8), done: make(chan struct{})}
	binary, err := os.Executable()
	require.NoError(t, err)
	c.cmd = exec.Command(binary, "-test.run=^TestPushHandoffProcessHelper$", "--", mode) //nolint:gosec,noctx // owned executable; cleanup kills and joins exact process
	c.cmd.Dir = dir                                                                      // Only child cwd selects the owned local store, never argv.
	c.cmd.Stderr = &c.stderr
	c.stdin, err = c.cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := c.cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, c.cmd.Start())
	t.Cleanup(func() { c.close(t) })
	go c.collect(stdout)
	return c
}
func (c *pushHandoffChild) collect(stdout io.Reader) {
	scan := bufio.NewScanner(stdout)
	for scan.Scan() {
		if !bytes.HasPrefix(scan.Bytes(), []byte("{")) {
			continue
		}
		var frame pushHandoffFrame
		if err := json.Unmarshal(scan.Bytes(), &frame); err != nil {
			c.scanErr = err
			break
		}
		c.frames <- frame
	}
	if c.scanErr == nil {
		c.scanErr = scan.Err()
	}
	// Finish reading before Wait closes stdout; both are joined by done.
	c.err = c.cmd.Wait()
	close(c.frames)
	close(c.done)
}
func (c *pushHandoffChild) frame(t *testing.T, kind string) pushHandoffFrame {
	t.Helper()
	select {
	case frame, ok := <-c.frames:
		require.True(t, ok, "owned child exited before %s", kind)
		require.Equal(t, kind, frame.Kind)
		require.Equal(t, c.cmd.Process.Pid, frame.PID)
		return frame
	case <-time.After(60 * time.Second):
		t.Fatalf("owned child did not signal %s", kind)
	}
	return pushHandoffFrame{}
}
func (c *pushHandoffChild) release(t *testing.T) {
	t.Helper()
	_, err := fmt.Fprintln(c.stdin, "continue")
	require.NoError(t, err)
}
func (c *pushHandoffChild) join(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
		require.NoError(t, c.scanErr, "owned frame reader")
		require.NoError(t, c.err, "owned child failed: %s", c.stderr.String())
	case <-time.After(60 * time.Second):
		t.Fatal("owned child failed to exit")
	}
}
func (c *pushHandoffChild) close(t *testing.T) {
	t.Helper()
	if err := c.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Errorf("close owned stdin: %v", err)
	}
	select {
	case <-c.done:
		return
	default:
	}
	if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("kill owned child: %v", err)
	}
	select {
	case <-c.done:
	case <-time.After(60 * time.Second):
		t.Error("killed owned child did not join")
	}
}
