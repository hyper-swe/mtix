// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// Real CLI fixtures isolate project and operator state and reap each owned wake process.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type wakeScriptFixture struct {
	root, project, binary, script string
	env                           []string
}

func newWakeScriptFixture(t *testing.T) wakeScriptFixture {
	t.Helper()
	f := wakeScriptFixture{root: t.TempDir()}
	f.project = filepath.Join(f.root, "project")
	f.binary = filepath.Join(f.root, "mtix")
	require.NoError(t, os.Mkdir(f.project, 0700))
	build := exec.CommandContext(t.Context(), "go", "build", "-o", f.binary, ".")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "build actual CLI: %s", output)
	operator := operatorTestHome(t)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	testBinary, err := os.Executable()
	require.NoError(t, err)
	f.env = []string{"PATH=" + f.root + ":/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(operator, "config"), "CODEX_HOME=" + filepath.Join(operator, "separate"), "MTIX_AUTHOR_ID=test", "WAKE_DIR=" + f.root, "WAKE_TEST_BINARY=" + testBinary, "WAKE_TEST_HELPER=1", "MTIX_BIN=" + f.binary, "HARNESS_BIN=" + filepath.Join(f.root, "claude"), "WAKE_AGENT_LOG=" + filepath.Join(f.root, "logs", "wake.log")}
	harness := "#!/bin/sh\nexec \"$WAKE_TEST_BINARY\" -test.run=^TestWakeAgentHarness_HelperProcess$ -- \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "claude"), []byte(harness), 0700))
	text, err := os.ReadFile(filepath.Join("..", "..", "examples", "hooks", "wake-agent.sh"))
	require.NoError(t, err)
	f.script = filepath.Join(f.root, "wake.sh")
	require.NoError(t, os.WriteFile(f.script, text, 0700))
	f.cli(t, "init", "--prefix", "TEST")
	f.cli(t, "create", "Handle addressed work")
	return f
}

func (f wakeScriptFixture) cli(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), f.binary, args...)
	cmd.Dir, cmd.Env = f.project, f.env
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "actual CLI %v: %s", args, output)
	return output
}

func (f wakeScriptFixture) event(t *testing.T, text string) {
	t.Helper()
	f.cli(t, "comment", "TEST-1", text, "--to", "TEST-agent")
}

type wakeScriptProcess struct {
	cmd    *exec.Cmd
	root   string
	output *syncBuffer
	done   chan struct{}
	err    error
}

func (f wakeScriptFixture) start(t *testing.T, extra ...string) *wakeScriptProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "/bin/sh", f.script, "TEST-agent")
	cmd.Dir = f.project
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(append([]string{}, f.env...), extra...)
	output := new(syncBuffer)
	cmd.Stdout, cmd.Stderr = output, output
	require.NoError(t, cmd.Start())
	p := &wakeScriptProcess{cmd: cmd, root: f.root, output: output, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	watcherDone := watchWakeCancellation(ctx, t, p)
	t.Cleanup(func() {
		cancel()
		<-p.done
		<-watcherDone
		if err := syscall.Kill(-cmd.Process.Pid, 0); err == nil {
			t.Errorf("owned wake descendants survived process completion")
			require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL))
		} else {
			require.ErrorIs(t, err, syscall.ESRCH)
			t.Logf("owned wake group pid=%d reaped", cmd.Process.Pid)
		}
	})
	return p
}

func (p *wakeScriptProcess) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-p.done:
		return p.err
	case <-time.After(65 * time.Second):
		t.Fatal("owned wake process did not finish")
		return nil
	}
}

func watchWakeCancellation(ctx context.Context, t *testing.T, p *wakeScriptProcess) <-chan struct{} {
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-p.done:
		case <-ctx.Done():
			if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Errorf("cancel owned wake group: %v", err)
			}
		}
	}()
	return watcherDone
}
