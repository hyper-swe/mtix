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

// outsideDir is a resolved temp directory outside the project.
func outsideDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

func TestPrepareExec_ApprovedScriptViaEquivalentRootPathIsPinned(t *testing.T) {
	f := newFixture(t, `[sh, "{A}/scripts/run.sh"]`, fixtureOptions{aliased: true})
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.NotEqual(t, filepath.Join(f.root, "scripts", "run.sh"), plan.Argv[1])
	require.NotContains(t, plan.Argv[1], "alias", "the staged copy runs, not the written path")
	staged, err := os.ReadFile(plan.Argv[1])
	require.NoError(t, err)
	require.Equal(t, prepareScript, string(staged))
}

func TestPrepareExec_UnapprovedFileViaEquivalentRootPathIsRefused(t *testing.T) {
	f := newFixture(t, `[sh, "{A}/scripts/later.sh"]`, fixtureOptions{aliased: true})
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	require.Nil(t, plan)
}

func TestPrepareExec_ChangedBytesViaEquivalentRootPathAreRefused(t *testing.T) {
	f := newFixture(t, `[sh, "{A}/scripts/run.sh"]`, fixtureOptions{aliased: true})
	_, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err, "the approved bytes launch")
	require.NoError(t, os.WriteFile(f.script, []byte("echo changed\n"), 0o700)) //nolint:gosec // test file
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.ErrorContains(t, err, "differs")
	require.Nil(t, plan)
}

func TestApprove_ProjectLinkIsRefusedWhicheverPathReachesIt(t *testing.T) {
	for name, command := range map[string]string{
		"plain root, link to a file":      `[sh, "{L}/tool"]`,
		"other root path, link to a file": `[sh, "{A}/tool"]`,
		"plain root, link to a dir":       `[sh, "{L}/sub/t.sh"]`,
		"other root path, link to a dir":  `[sh, "{A}/sub/t.sh"]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, command, fixtureOptions{aliased: true, unapproved: true})
			target := outsideDir(t)
			require.NoError(t, os.WriteFile(filepath.Join(target, "t.sh"), []byte("echo outside\n"), 0o700)) //nolint:gosec // test file
			require.NoError(t, os.Symlink(filepath.Join(target, "t.sh"), filepath.Join(f.root, "tool")))
			require.NoError(t, os.Symlink(target, filepath.Join(f.root, "sub")))
			require.ErrorContains(t, f.approvals.Approve("wake", true), "symlink-free")
		})
	}
}

func TestPrepareExec_OutsideLinkIntoProjectWithParentSegmentIsNotPinnedOrRun(t *testing.T) {
	outside := outsideDir(t)
	f := newFixture(t, "[sh, "+filepath.Join(outside, "lnk")+"/../../DECOY]", fixtureOptions{extra: map[string]string{"a/b/keep": "x"}, unapproved: true})
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "DECOY"), []byte("echo inside\n"), 0o700))               //nolint:gosec // test file
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(f.root), "DECOY"), []byte("echo beside\n"), 0o700)) //nolint:gosec // test file
	require.NoError(t, os.Symlink(filepath.Join(f.root, "a", "b"), filepath.Join(outside, "lnk")))
	require.NoError(t, f.approvals.Approve("wake", true))
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.ErrorContains(t, err, "not an approved project file", "a file reached through an outside link is neither pinned nor run")
	require.Nil(t, plan)
}

func TestPrepareExec_ProgramWithParentSegmentIsRefusedEvenWhenPinned(t *testing.T) {
	for _, command := range []string{"[scripts/../scripts/run.sh]", `["{A}/scripts/../scripts/run.sh"]`} {
		t.Run(command, func(t *testing.T) {
			f := newFixture(t, command, fixtureOptions{aliased: true})
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.ErrorContains(t, err, "parent-directory segment")
			require.Nil(t, plan)
		})
	}
}

func TestRootRelative_DecidesByFileIdentityNotText(t *testing.T) {
	root := linkLayout(t)
	other := outsideDir(t)
	via := filepath.Join(other, "via")
	require.NoError(t, os.Symlink(root, via))
	for name, tc := range map[string]struct {
		path   string
		rel    string
		inside bool
	}{
		"below the root":                         {filepath.Join(root, "deep", "marker"), filepath.Join("deep", "marker"), true},
		"the root":                               {root, ".", true},
		"a path through a link to root":          {filepath.Join(via, "deep", "new"), filepath.Join("deep", "new"), true},
		"the root through a link":                {via, ".", true},
		"the parent of the root":                 {filepath.Dir(root), "", false},
		"a sibling whose name has a root prefix": {root + "-sibling", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			rel, inside, decided := rootRelative(root, tc.path)
			require.True(t, decided)
			require.Equal(t, tc.inside, inside)
			require.Equal(t, tc.rel, rel)
		})
	}
}

func TestRootRelative_UndecidableIsReported(t *testing.T) {
	root := linkLayout(t)
	_, _, decided := rootRelative(filepath.Join(root, "absent-root"), root)
	require.False(t, decided, "an unreadable root decides nothing")
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind the superuser")
	}
	locked := filepath.Join(root, "locked")
	require.NoError(t, os.Mkdir(locked, 0o700))
	require.NoError(t, os.Chmod(locked, 0o000))       //nolint:gosec // test directory
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // test cleanup
	_, _, decided = rootRelative(root, filepath.Join(locked, "x", "y"))
	require.False(t, decided, "a permission error is not taken for absence")
}

func TestPhysicalLocation_ClassesEveryLinkShape(t *testing.T) {
	root := linkLayout(t)
	outside := outsideDir(t)
	via := filepath.Join(outside, "via")
	require.NoError(t, os.Symlink(root, via))
	into := filepath.Join(outside, "into")
	require.NoError(t, os.Symlink(filepath.Join(root, "deep"), into))
	away := filepath.Join(outside, "away")
	require.NoError(t, os.Symlink(outside, away))
	loopA, loopB := filepath.Join(outside, "la"), filepath.Join(outside, "lb")
	require.NoError(t, os.Symlink(loopB, loopA))
	require.NoError(t, os.Symlink(loopA, loopB))
	for name, tc := range map[string]struct {
		path                    string
		inside, linked, foreign bool
		complete                bool
	}{
		"plain relative":               {"deep/marker", true, false, false, true},
		"relative with dot segments":   {"deep/./a/../marker", true, false, false, true},
		"through a link to the root":   {via + "/deep/marker", true, false, false, true},
		"through a link inside":        {"lnk/x", true, true, false, true},
		"through a link into a subdir": {into + "/marker", true, false, true, true},
		"through a link away":          {away + "/x", false, false, false, true},
		"a link loop":                  {loopA + "/x", false, false, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := physicalLocation(root, tc.path)
			require.Equal(t, tc.complete, got.Complete)
			if !tc.complete {
				return
			}
			require.Equal(t, tc.inside, got.Inside)
			require.Equal(t, tc.linked, got.Linked)
			require.Equal(t, tc.foreign, got.Foreign)
		})
	}
}
