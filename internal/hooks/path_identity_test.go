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

func TestPrepareExec_ApprovedScriptViaAlternateRootSpellingIsPinned(t *testing.T) {
	f := newFixture(t, `[sh, "{A}/scripts/run.sh"]`, fixtureOptions{aliased: true})
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err, "an approved script reached through an alias of the root runs")
	require.NotEqual(t, filepath.Join(f.root, "scripts", "run.sh"), plan.Argv[1])
	require.NotContains(t, plan.Argv[1], "alias", "the staged copy runs, not the written path")
	require.FileExists(t, plan.Argv[1])
}

func TestPrepareExec_UnapprovedFileViaAlternateRootSpellingIsRefused(t *testing.T) {
	f := newFixture(t, `[sh, "{A}/scripts/later.sh"]`, fixtureOptions{aliased: true})
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	require.Nil(t, plan)
}

func TestApprove_ScriptViaAlternateRootSpellingIsPartOfTheDigest(t *testing.T) {
	f := newFixture(t, `[sh, "{A}/scripts/run.sh"]`, fixtureOptions{aliased: true})
	require.NoError(t, os.WriteFile(f.script, []byte("echo changed\n"), 0o700)) //nolint:gosec // test file
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "changed bytes are no longer the approved ones")
	require.Nil(t, plan)
}

func TestRootRelative_FindsTheRootByIdentity(t *testing.T) {
	root := linkLayout(t)
	rel, ok := rootRelative(root, filepath.Join(root, "deep", "marker"))
	require.True(t, ok)
	require.Equal(t, filepath.Join("deep", "marker"), rel)
	rel, ok = rootRelative(root, root)
	require.True(t, ok)
	require.Equal(t, ".", rel)
	_, ok = rootRelative(root, filepath.Dir(root))
	require.False(t, ok)
}
