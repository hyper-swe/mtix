// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package procinfo_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

func TestIdentify_ObservedProcesses_MapToHarness(t *testing.T) {
	tests := []struct {
		name string
		proc procinfo.Proc
		want string
	}{
		// Observed on macOS: the kernel name of Claude Code is its version string because
		// `claude` is a symlink to versions/<version>; argv[0] and the exec path carry the identity.
		{"claude native on macOS", procinfo.Proc{Name: "2.1.296", Argv0: "claude", Exe: "/Users/u/.local/share/claude/versions/2.1.296"}, "claude-code"},
		{"claude by exec path only", procinfo.Proc{Name: "2.1.296", Exe: "/home/u/.local/share/claude/versions/2.1.296"}, "claude-code"},
		{"claude by kernel name", procinfo.Proc{Name: "claude"}, "claude-code"},
		{"codex", procinfo.Proc{Name: "codex", Argv0: "codex", Exe: "/opt/homebrew/bin/codex"}, "codex"},
		{"pi", procinfo.Proc{Name: "pi", Argv0: "pi"}, "pi"},
		{"login-style argv0", procinfo.Proc{Name: "x", Argv0: "-Codex"}, "codex"},
		{"node is not a harness", procinfo.Proc{Name: "node", Argv0: "node", Exe: "/usr/local/bin/node"}, ""},
		{"version string alone", procinfo.Proc{Name: "2.1.296"}, ""},
		{"a directory named pi is not pi", procinfo.Proc{Name: "python", Exe: "/home/u/pi/bin/python"}, ""},
		{"two harnesses is ambiguous", procinfo.Proc{Name: "codex", Argv0: "claude"}, ""},
		{"empty", procinfo.Proc{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { assert.Equal(t, tt.want, procinfo.Identify(tt.proc)) })
	}
}

func TestFindSession_NodeChildOfKnownHarness_ResolvesToTheHarness(t *testing.T) {
	procs := table{
		90: {PPID: 80, Name: "bash", StartTime: "a"},
		80: {PPID: 50, Name: "node", Argv0: "node", StartTime: "n"},
		50: {PPID: 10, Name: "codex", Argv0: "codex", StartTime: "s50"},
	}
	found, sure := procinfo.FindSession(procs.read, 90)
	assert.True(t, sure)
	assert.Equal(t, 50, found.PID)
	assert.Equal(t, "codex", found.Harness)
	assert.Equal(t, "s50", found.StartTime)
}

func TestFindSession_NodeWithUnknownParent_StaysTheNodeProcess(t *testing.T) {
	procs := table{
		80: {PPID: 50, Name: "node", Argv0: "node", StartTime: "n"},
		50: {PPID: 10, Name: "launchd-ish", StartTime: "s50"},
	}
	found, sure := procinfo.FindSession(procs.read, 80)
	assert.True(t, sure)
	assert.Equal(t, 80, found.PID)
	assert.Empty(t, found.Harness, "unknown stays unknown and fails safe")
}

func TestFindSession_RealClaudeProcess_IdentifiedByArgv0(t *testing.T) {
	procs := table{
		91: {PPID: 26, Name: "/bin/bash", StartTime: "b"},
		26: {PPID: 10, Name: "2.1.296", Argv0: "claude", Exe: "/Users/u/.local/share/claude/versions/2.1.296", StartTime: "s26"},
	}
	found, sure := procinfo.FindSession(procs.read, 91)
	assert.True(t, sure)
	assert.Equal(t, "claude-code", found.Harness)
	assert.Equal(t, 26, found.PID)
}

func TestInfo_OwnProcess_ReportsArgv0AndExecutable(t *testing.T) {
	p, err := procinfo.Info(os.Getpid())
	if errors.Is(err, procinfo.ErrUnsupported) {
		t.Skip("unsupported platform")
	}
	require.NoError(t, err)
	exe, err := os.Executable()
	require.NoError(t, err)
	assert.NotEmpty(t, p.Argv0)
	assert.Equal(t, filepath.Base(os.Args[0]), filepath.Base(p.Argv0))
	wantExe, err := filepath.EvalSymlinks(exe)
	require.NoError(t, err)
	gotReal, err := filepath.EvalSymlinks(p.Exe)
	require.NoError(t, err)
	assert.Equal(t, wantExe, gotReal)
}

func TestParseProcArgs2_ExecPathPaddingAndArgv0(t *testing.T) {
	raw := append([]byte{3, 0, 0, 0}, []byte("/opt/claude/versions/2.1.296\x00\x00\x00claude\x00--flag\x00x\x00")...)
	exe, argv0, ok := procinfo.ParseProcArgs2(raw)
	assert.True(t, ok)
	assert.Equal(t, "/opt/claude/versions/2.1.296", exe)
	assert.Equal(t, "claude", argv0)

	for _, bad := range [][]byte{nil, {1, 0, 0}, append([]byte{1, 0, 0, 0}, []byte("/only/exec")...)} {
		_, _, ok = procinfo.ParseProcArgs2(bad)
		assert.False(t, ok)
	}
}
