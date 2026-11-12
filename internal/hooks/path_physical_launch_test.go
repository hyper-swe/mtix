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

// outsideLinkLayout makes "<sibling>/into", a link from outside the project to
// a project subdirectory, and returns the sibling directory.
func outsideLinkLayout(t *testing.T, f prepareFixture) string {
	t.Helper()
	sibling := filepath.Join(filepath.Dir(f.root), "sib")
	require.NoError(t, os.Mkdir(sibling, 0o700))
	require.NoError(t, os.Symlink(filepath.Join(f.root, "deep"), filepath.Join(sibling, "into")))
	return sibling
}

func TestPrepareExec_ProjectFileReachedByOutsideLinkAndParentSegmentsIsNeverRun(t *testing.T) {
	extra := map[string]string{"deep/keep": "x", "tool.awk": "BEGIN { print 1 }\n"}
	const route = "../sib/into/../../{N}/tool.awk"
	for name, command := range map[string]string{
		"absolute argument": `[sh, scripts/run.sh, "{L}/` + route + `"]`,
		"relative argument": `[sh, scripts/run.sh, "` + route + `"]`,
		"option value":      `[sh, scripts/run.sh, "--file={L}/` + route + `"]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, command, fixtureOptions{extra: extra, unapproved: true})
			outsideLinkLayout(t, f)
			require.NoError(t, f.approvals.Approve("wake", true))
			require.NoError(t, os.WriteFile(filepath.Join(f.root, "tool.awk"), []byte("BEGIN { print 2 }\n"), 0o700)) //nolint:gosec // test file
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.ErrorContains(t, err, "not an approved file", "bytes changed after approval must not run")
			require.Nil(t, plan)
		})
	}
}

func TestPrepareExec_RelativeOperandStagesTheClassifiedFileNotTheWrittenText(t *testing.T) {
	f := newFixture(t, `[sh, "../oq/via/../{N}/x.sh"]`, fixtureOptions{extra: map[string]string{"x.sh": "echo INSIDE\n"}, unapproved: true})
	oq := filepath.Join(filepath.Dir(f.root), "oq")
	require.NoError(t, os.MkdirAll(filepath.Join(oq, filepath.Base(f.root)), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(oq, filepath.Base(f.root), "x.sh"), []byte("echo DECOY\n"), 0o700)) //nolint:gosec // test file
	require.NoError(t, os.Symlink(f.root, filepath.Join(oq, "via")))
	require.NoError(t, f.approvals.Approve("wake", true))
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	staged, err := os.ReadFile(plan.Argv[1])
	require.NoError(t, err)
	require.Equal(t, "echo INSIDE\n", string(staged), "the file the OS reads is the file that was approved")
}

func TestPrepareExec_OperandUnderUnreadableDirectoryIsRefusedWithAnHonestReason(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind the superuser")
	}
	outside := outsideDir(t)
	locked := filepath.Join(outside, "locked")
	require.NoError(t, os.Mkdir(locked, 0o700))
	f := newFixture(t, "[sh, scripts/run.sh, "+filepath.Join(locked, "data")+"]", fixtureOptions{})
	require.NoError(t, os.Chmod(locked, 0o000))       //nolint:gosec // test directory
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // test cleanup
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.ErrorContains(t, err, "could not be judged")
	require.NotContains(t, err.Error(), "link expansion limit")
	require.Nil(t, plan)
}
