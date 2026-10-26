// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeStubGit writes an executable shell stub that runs body as its program.
func writeStubGit(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("shell stub Git is unavailable on this platform")
	}
	path := filepath.Join(t.TempDir(), "stub-git")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700)) //nolint:gosec // owned test stub must be executable
	return path
}

func TestBoardGitFixture_HungGit_FailsWithinBoundNamingSubcommand(t *testing.T) {
	stub := writeStubGit(t, "exec sleep 30")
	start := time.Now()
	_, err := runBoardGit(stub, t.TempDir(), t.TempDir(), 300*time.Millisecond, "clone", "remote.git", "writer")
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Less(t, elapsed, 10*time.Second, "a hung git must fail within the bound")
	msg := err.Error()
	require.Contains(t, msg, "git clone", "failure must name the subcommand")
	require.Contains(t, msg, "after", "failure must report the elapsed time")
	require.Contains(t, msg, "300ms", "failure must report the bound")
	m := regexp.MustCompile(`after (\S+) \(bound`).FindStringSubmatch(msg)
	require.Len(t, m, 2, "failure must carry an elapsed value")
	reported, perr := time.ParseDuration(m[1])
	require.NoError(t, perr)
	require.GreaterOrEqual(t, reported, 300*time.Millisecond, "elapsed cannot be below the bound that killed it")
	require.LessOrEqual(t, reported, elapsed+time.Millisecond, "elapsed cannot exceed the measured wall time")
}

func TestBoardGitFixture_ForkedSleepOutlivingGit_StillFailsNearBound(t *testing.T) {
	// The sleep is a child of the shell, not exec'd: killing only the shell leaves
	// the child holding the output pipe, which stretched the bound to the sleep.
	stub := writeStubGit(t, "sleep 30\necho unreachable")
	start := time.Now()
	_, err := runBoardGit(stub, t.TempDir(), t.TempDir(), 300*time.Millisecond, "fetch")
	elapsed := time.Since(start)
	require.Error(t, err)
	require.Less(t, elapsed, 300*time.Millisecond+boardGitWaitDelay+2*time.Second, "bound plus a small margin")
}

func TestBoardGitFixture_BoardGit_UsesFixtureBound(t *testing.T) {
	var gotBound time.Duration
	var gotArgs []string
	fake := func(_, _, _ string, bound time.Duration, args ...string) (string, error) {
		gotBound, gotArgs = bound, args
		return "ok", nil
	}
	require.Equal(t, "ok", boardGitUsing(t, fake, t.TempDir(), "status"))
	require.Equal(t, boardGitFixtureBound, gotBound)
	require.Equal(t, []string{"status"}, gotArgs)
}

func TestBoardGitFixture_FastGit_ReturnsOutputWithinBound(t *testing.T) {
	stub := writeStubGit(t, "echo fixture-ok")
	out, err := runBoardGit(stub, t.TempDir(), t.TempDir(), 5*time.Second, "status")
	require.NoError(t, err)
	require.Equal(t, "fixture-ok", strings.TrimSpace(out))
}

func TestBoardGitFixture_FailingGit_ErrorNamesSubcommandAndOutput(t *testing.T) {
	stub := writeStubGit(t, "echo boom >&2; exit 3")
	_, err := runBoardGit(stub, t.TempDir(), t.TempDir(), 5*time.Second, "commit", "-m", "x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "git commit")
	require.Contains(t, err.Error(), "boom")
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "the exec error must stay on the chain")
	require.Equal(t, 3, exitErr.ExitCode())
}

func TestBoardGitFixture_Bound_IsGenerousForLoadedRunners(t *testing.T) {
	require.Equal(t, 60*time.Second, boardGitFixtureBound)
	require.LessOrEqual(t, boardGitFixtureBound, 5*time.Minute, "a hung git must still fail in bounded time")
}
