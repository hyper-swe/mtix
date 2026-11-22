// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// pathLinkLayout builds a project root with an executable under bin/, a fixed PATH
// directory outside the project that links into bin/, and a plain PATH
// directory outside the project that holds its own executables.
type pathLinkLayout struct {
	root, linked, plain string
}

func newPathLinkLayout(t *testing.T, names ...string) pathLinkLayout {
	t.Helper()
	resolve := func(dir string) string {
		physical, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)
		return physical
	}
	l := pathLinkLayout{root: resolve(t.TempDir()), plain: resolve(t.TempDir())}
	require.NoError(t, os.Mkdir(filepath.Join(l.root, "bin"), 0o700))
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(l.root, "bin", name), []byte("#!/bin/sh\n"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(l.plain, name), []byte("#!/bin/sh\n"), 0o700))
	}
	l.linked = filepath.Join(resolve(t.TempDir()), "linkbin")
	require.NoError(t, os.Symlink(filepath.Join(l.root, "bin"), l.linked))
	return l
}

func TestKnownProgramPin_PathDirectoryLinkedIntoProject_IsNotKnown(t *testing.T) {
	l := newPathLinkLayout(t, "python3", "env")
	for _, tc := range []struct {
		name    string
		program string
		known   func(string) bool
	}{
		{"absolute interpreter", filepath.Join(l.linked, "python3"), func(p string) bool { return reviewBaseName(p) == "python3" }},
		{"bare interpreter", "python3", func(p string) bool { return reviewBaseName(p) == "python3" }},
		{"absolute env", filepath.Join(l.linked, "env"), isEnv},
		{"bare env", "env", isEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pin, ok := knownProgramPinIn(l.root, []string{l.linked}, tc.program, tc.known)
			require.False(t, ok, "a program that resolves inside the project is never known by its name; pin %q", pin)
		})
	}
}

func TestKnownProgramPin_FileLinkedIntoProjectInPlainPathDirectory_IsNotKnown(t *testing.T) {
	l := newPathLinkLayout(t, "python3")
	target := filepath.Join(l.root, "bin", "python3")
	require.NoError(t, os.Remove(filepath.Join(l.plain, "python3")))
	require.NoError(t, os.Symlink(target, filepath.Join(l.plain, "python3")))
	known := func(p string) bool { return reviewBaseName(p) == "python3" }
	for _, program := range []string{filepath.Join(l.plain, "python3"), "python3"} {
		_, ok := knownProgramPinIn(l.root, []string{l.plain}, program, known)
		require.False(t, ok, program)
	}
}

func TestKnownProgramPin_PathDirectoryOutsideProject_StaysKnown(t *testing.T) {
	l := newPathLinkLayout(t, "python3")
	known := func(p string) bool { return reviewBaseName(p) == "python3" }
	pin, ok := knownProgramPinIn(l.root, []string{l.plain}, filepath.Join(l.plain, "python3"), known)
	require.True(t, ok)
	require.Equal(t, filepath.Join(l.plain, "python3"), pin)
	pin, ok = knownProgramPinIn(l.root, []string{l.plain}, "python3", known)
	require.True(t, ok)
	require.Equal(t, "python3", pin, "a bare name is returned as written")
	_, ok = knownProgramPinIn(l.root, []string{l.plain}, "python3.bat", func(string) bool { return true })
	require.True(t, ok, "a bare name that is not found on the fixed PATH keeps the name decision")
}

func TestClassifyProgram_ProgramResolvingInsideProject_FixSaysToPinTheScript(t *testing.T) {
	l := newPathLinkLayout(t, "tool")
	h := Hook{Name: "wake", Exec: &ExecConfig{Command: []string{filepath.Join(l.linked, "tool")}}}
	review := classifyProgram(h, &hookInputs{root: l.root, program: 0, script: -1})
	require.Equal(t, ClassOther, review.Class)
	require.True(t, review.Needed)
	require.Equal(t, filepath.Join(l.root, "bin", "tool"), review.Program)
	require.Contains(t, review.Detail, "resolves inside the project")
	require.Contains(t, review.Fix, "resolves inside the project")
	require.NotContains(t, review.Fix, "trust --program")
}
