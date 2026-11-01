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

// linkLayout builds root/deep/a/b, root/deep/marker, and root/lnk -> root/deep/a/b.
// Followed physically, lnk/../../marker is root/deep/marker; cleaned as text it
// would be a sibling of the project root.
func linkLayout(t *testing.T) (root string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "deep", "a", "b"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "deep", "marker"), []byte("x"), 0o700)) //nolint:gosec // test file
	require.NoError(t, os.Symlink(filepath.Join(root, "deep", "a", "b"), filepath.Join(root, "lnk")))
	return root
}

func TestResolveDeepest_FollowsLinksBeforeParentSegments(t *testing.T) {
	root := linkLayout(t)
	want := filepath.Join(root, "deep", "marker")
	require.Equal(t, want, resolveDeepest(root+"/lnk/../../marker", 0))
	require.Equal(t, filepath.Join(root, "deep", "a", "b", "new", "file"), resolveDeepest(root+"/lnk/new/file", 0), "a path that does not exist yet is judged where it would land")
	require.Equal(t, filepath.Join(root, "deep", "a"), resolveDeepest(root+"/lnk/..", 0))
}

func TestResolveDeepest_LinkLoopTerminates(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "a")))
	require.NoError(t, os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "b")))
	require.NotPanics(t, func() { _ = resolveDeepest(root+"/a/../x", 0) })
}

func TestProjectPath_ParentSegmentAfterALinkIsJudgedWhereItLands(t *testing.T) {
	root := linkLayout(t)
	for name, value := range map[string]string{
		"absolute": root + "/lnk/../../marker",
		"relative": "lnk/../../marker",
	} {
		t.Run(name, func(t *testing.T) {
			resolved, _, inside := projectPath(root, root, value)
			require.Equal(t, filepath.Join(root, "deep", "marker"), resolved)
			require.True(t, inside, "the file the OS runs is inside the project")
		})
	}
	_, _, outside := projectPath(root, root, "/bin/sh")
	require.False(t, outside)
}

func hookWithProgram(t *testing.T, program string) prepareFixture {
	t.Helper()
	f := newFixture(t, "["+program+", x]", fixtureOptions{unapproved: true})
	require.NoError(t, f.approvals.Approve("wake", true))
	return f
}

func TestPrepareExec_ProgramPathWithParentSegmentIsRefused(t *testing.T) {
	root := linkLayout(t)
	tool := filepath.Join(root, "deep", "tool")
	require.NoError(t, os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // test program
	for name, program := range map[string]string{
		"absolute with link":      root + "/lnk/../../tool",
		"absolute benign":         "/usr/bin/../bin/cat",
		"relative parent segment": "../x/cat",
	} {
		t.Run(name, func(t *testing.T) {
			f := hookWithProgram(t, program)
			_, err := f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err)
			require.Contains(t, err.Error(), "parent-directory")
			require.Contains(t, err.Error(), `"wake"`, "the error names the hook")
		})
	}
}

func TestPrepareExec_LegitimateRelativeScriptsStillLaunch(t *testing.T) {
	for _, command := range []string{"[scripts/run.sh]", "[./scripts/run.sh]", "[sh, scripts/run.sh]", "[sh, ./scripts/run.sh]"} {
		t.Run(command, func(t *testing.T) {
			f := newPrepareFixture(t, command)
			_, err := f.approvals.PrepareExec(f.hook(t))
			require.NoError(t, err)
		})
	}
}
