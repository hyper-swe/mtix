// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// A child process opens the caller's real board and executes the addressed-send
// CLI command. Only its duration is injected; the transport and lock are real.
func TestAddressedSendChildProcessHelper(t *testing.T) {
	mode := os.Getenv("MTIX_ADDRESSED_CHILD_TEST")
	if mode == "" {
		return
	}
	bound := 500 * time.Millisecond
	if mode == "kill" {
		bound = 30 * time.Second
	}
	cmd := newSyncPushCmdWithTimeout(bound)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, initApp(cmd, ""))
	require.NotNil(t, app.store)
	t.Cleanup(func() { require.NoError(t, app.store.Close()) })
	cmd.SetArgs([]string{"--on-addressed-send", "--insecure-tls"})
	require.Error(t, cmd.ExecuteContext(context.Background()))
}

// A comment caller must return while its real child is blocked in the hub read.
// Killing that child releases the kernel lock, and a later child can take it,
// time out normally, and persist its failure without losing the comments.
func TestAddressedSendChild_StalledProcessDoesNotBlockComment_KillFreesLock(t *testing.T) {
	dsn, accepted := childTimeoutHub(t)
	t.Setenv(transport.EnvDSN, dsn)
	initTestApp(t)
	id := seedOneNode(t)
	var child *exec.Cmd
	var output bytes.Buffer
	mode := "kill"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	app.detachedSpawn = func(exe, dir string, argv []string) error {
		assert.Equal(t, childPushArgv(), argv)
		child = exec.CommandContext(ctx, exe, "-test.run=^TestAddressedSendChildProcessHelper$", "-test.timeout=10s")
		child.Dir = dir
		child.Env = append(hostguard.Environ(), "MTIX_ADDRESSED_CHILD_TEST="+mode)
		child.Stdout, child.Stderr = &output, &output
		if err := child.Start(); err != nil {
			return err
		}
		proc := child
		t.Cleanup(func() { stopAddressedTestChild(t, proc) })
		return nil
	}
	start := time.Now()
	require.NoError(t, runComment(id, "first message", "agent-b"))
	assert.Less(t, time.Since(start), 2*time.Second, "a comment caller waited for its stalled child")
	require.NotNil(t, child)
	first := child
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the child did not reach the hung hub")
	}
	lock, err := pushlock.Acquire(app.mtixDir)
	if lock != nil {
		require.NoError(t, lock.Release())
	}
	require.ErrorIs(t, err, pushlock.ErrLockHeld)
	require.NoError(t, first.Process.Kill())
	require.Error(t, first.Wait(), "killing the stalled child must end it")
	lock, err = pushlock.Acquire(app.mtixDir)
	require.NoError(t, err, "process death stranded the push lock")
	require.NoError(t, lock.Release())
	mode = "timeout"
	output.Reset()
	require.NoError(t, runComment(id, "second message", "agent-b"))
	require.NotSame(t, first, child)
	require.NoError(t, child.Wait(), output.String())
	assert.NoError(t, ctx.Err())
	ok, class := waitAddressedPushRecord(t)
	assert.False(t, ok)
	assert.Equal(t, "timeout", class)
	lock, err = pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	require.NoError(t, lock.Release())
	comments, err := app.store.InboxList(context.Background(), "agent-b")
	require.NoError(t, err)
	assert.Len(t, comments, 2, "both messages stay durable after interrupted pushes")
	assert.Equal(t, filepath.Dir(app.mtixDir), child.Dir)
}

func stopAddressedTestChild(t *testing.T, child *exec.Cmd) {
	t.Helper()
	if child.ProcessState != nil {
		return
	}
	if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("stop test child: %v", err)
	}
	if err := child.Wait(); err != nil {
		var exit *exec.ExitError
		assert.ErrorAs(t, err, &exit, "reap test child")
	}
}
