// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScriptOperandIndex(t *testing.T) {
	cases := []struct {
		command []string
		want    int
	}{
		{[]string{"scripts/run.sh"}, 0},
		{[]string{"sh", "run.sh"}, 1},
		{[]string{"sh", "-e", "run.sh"}, 2},
		{[]string{"sh", "-o", "pipefail", "run.sh"}, 3},
		{[]string{"bash", "--", "run.sh"}, 2},
		{[]string{"env", "sh", "run.sh"}, 2},
		{[]string{"env", "-u", "X", "A=b", "sh", "run.sh"}, 5},
		{[]string{"env", "./tool"}, 1},
		{[]string{"/usr/bin/env", "python3", "-W", "ignore", "run.py"}, 4},
		{[]string{"python3.12", "run.py"}, 1},
		{[]string{"node", "-r", "mod", "run.js"}, 3},
		{[]string{"perl", "run.pl"}, 1},
		{[]string{"ruby", "run.rb"}, 1},
		{[]string{"zsh", "run.zsh"}, 1},
		{[]string{"sh", "-c", "echo hi"}, -1},
		{[]string{"sh", "-ec", "echo hi"}, -1},
		{[]string{"python3", "-m", "pkg"}, -1},
		{[]string{"node", "-e", "1"}, -1},
		{[]string{"cat", "file"}, -1},
		{[]string{"sh"}, -1},
	}
	for _, c := range cases {
		require.Equal(t, c.want, scriptOperandIndex(c.command), strings.Join(c.command, " "))
	}
}

func TestPrepareExec_ScriptPositionAlwaysPinned(t *testing.T) {
	for _, command := range []string{
		"[scripts/later.sh]",
		"[sh, scripts/later.sh]",
		"[sh, -e, scripts/later.sh]",
		"[env, sh, scripts/later.sh]",
		"[env, -u, FOO, A=b, sh, later.sh]",
		"[sh, later.sh]",
		"[sh, scripts]",
	} {
		t.Run(command, func(t *testing.T) {
			f := newPrepareFixture(t, command)
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err, "a script operand with no approved file starts nothing")
			require.Nil(t, plan)
		})
	}
}

func TestPrepareExec_ScriptPositionApprovedFileRuns(t *testing.T) {
	for _, command := range []string{
		"[sh, -o, pipefail, scripts/run.sh]",
		"[env, A=b, sh, scripts/run.sh]",
		"[sh, -c, echo hi]",
	} {
		t.Run(command, func(t *testing.T) {
			f := newPrepareFixture(t, command)
			_, err := f.approvals.PrepareExec(f.hook(t))
			require.NoError(t, err)
		})
	}
}

func TestPrepareExec_NonScriptOperandsPassUnchanged(t *testing.T) {
	for _, operand := range []string{"https://example.invalid/x", "--config=conf/x.yaml", "missing/file.txt", "plainword", "scripts"} {
		t.Run(operand, func(t *testing.T) {
			f := newPrepareFixture(t, "[cat, "+operand+"]")
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.NoError(t, err)
			require.Equal(t, operand, plan.Argv[1])
		})
	}
}

func TestPrepareExec_OptionValueFileIsPinned(t *testing.T) {
	f := newPrepareFixture(t, "[cat, --config=scripts/run.sh]")
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(plan.Argv[1], "--config="))
	copyPath := strings.TrimPrefix(plan.Argv[1], "--config=")
	require.NotEqual(t, f.script, copyPath)
	require.FileExists(t, copyPath)
	require.NoError(t, os.WriteFile(f.script, []byte("# edited\n"), 0o700))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "an edited option-value file needs a new approval")
}

func TestPrepareExec_ExistingUnapprovedOperandFileRefused(t *testing.T) {
	f := newPrepareFixture(t, "[cat, data/in.txt]")
	require.NoError(t, os.Mkdir(filepath.Join(f.root, "data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "data", "in.txt"), []byte("new"), 0o600))
	_, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
}

func TestPrepareExec_OperandLinkIntoProjectRefused(t *testing.T) {
	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(outside, "link.sh")
	f := newPrepareFixture(t, "[sh, "+link+"]")
	require.NoError(t, os.Symlink(f.script, link))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "a link from outside to an unpinned project file is refused")

	link2 := filepath.Join(outside, "link2.sh")
	g := newPrepareFixture(t, "[cat, "+link2+"]")
	require.NoError(t, os.Symlink(g.script, link2))
	_, err = g.approvals.PrepareExec(g.hook(t))
	require.Error(t, err, "also in a non-script position")
}

func TestPrepareExec_PathLinkIntoProjectRefusedWithResolvedTarget(t *testing.T) {
	f := newPrepareFixture(t, "[tool]")
	bin := filepath.Join(f.root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	target := filepath.Join(bin, "tool")
	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Symlink(target, filepath.Join(outside, "tool")))
	t.Setenv("PATH", outside+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), target, "the message names the resolved target")
}

func TestPrepareExec_LinkedCopiesDirectoryRefused(t *testing.T) {
	f := newPrepareFixture(t, "[scripts/run.sh]")
	require.NoError(t, f.state.Ensure())
	opDir, err := f.state.Path()
	require.NoError(t, err)
	elsewhere, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(opDir, execCopiesDir)))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	entries, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing was written through the link")
}

func TestPrepareExec_ScriptPathIsFirstScript(t *testing.T) {
	f := newPrepareFixtureWith(t, "[sh, scripts/run.sh, scripts/second.sh]", map[string]string{"scripts/second.sh": "# second\n"})
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.Equal(t, f.script, plan.ScriptPath)
}
