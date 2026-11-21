// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// linkChain builds a chain of n links in a fresh directory that ends at target
// and returns the head of the chain. The chain is longer than the number of
// links one resolution follows.
func linkChain(t *testing.T, n int, target string) string {
	t.Helper()
	dir := t.TempDir()
	for i := n - 1; i >= 0; i-- {
		next := target
		if i < n-1 {
			next = filepath.Join(dir, fmt.Sprintf("l%d", i+1))
		}
		require.NoError(t, os.Symlink(next, filepath.Join(dir, fmt.Sprintf("l%d", i))))
	}
	return filepath.Join(dir, "l0")
}

func TestResolveDeepest_PastTheLinkLimitIsNeverCountedAsResolved(t *testing.T) {
	_, realDir := linkedPathDir(t, "sh")
	// Relative hops under a physical directory: the limit is reached on the
	// chain itself, not on a link in the temporary directory's own path.
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	n := maxLinkExpansions + 6
	for i := n - 1; i >= 0; i-- {
		next := realDir
		if i < n-1 {
			next = fmt.Sprintf("l%d", i+1)
		}
		require.NoError(t, os.Symlink(next, filepath.Join(base, fmt.Sprintf("l%d", i))))
	}
	got := resolveDeepest(filepath.Join(base, "l0"), 0)
	resolved, err := filepath.EvalSymlinks(got)
	require.NoError(t, err)
	require.NotEqual(t, got, resolved, "a chain past the limit must not look like a physical directory")
}

func TestStrict_KnownSlotBehindALinkChainPastTheLimitIsNotKnown(t *testing.T) {
	for _, name := range []string{"env", "sh"} {
		_, realDir := linkedPathDir(t, name)
		head := linkChain(t, maxLinkExpansions+6, realDir)
		root, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		known := knownEnvWrapper
		command := "[" + filepath.Join(head, name) + ", sh, scripts/run.sh]"
		if name == "sh" {
			known = knownInterpreterProgram
			command = "[" + filepath.Join(head, name) + ", scripts/run.sh]"
		}
		require.False(t, known(root, filepath.Join(head, name)), "a long chain to the PATH directory is not known: %s", name)
		f := strictFixture(t, command)
		_, err = f.approvals.PrepareExec(f.hook(t))
		require.Error(t, err, "the slot needs an approval, it is not pinned: %s", name)
		// The program is refused before strict judgement: either reason is a
		// refusal, and the link-expansion refusal is pinned on its own below.
		require.Regexp(t, "strict program mode|could not be judged", err.Error())
		require.Contains(t, err.Error(), "could not be judged", "refused for the right reason: %s", name)
	}
}

func TestPinKnownSlots_RefusesADirectoryThatIsStillALink(t *testing.T) {
	_, realDir := linkedPathDir(t, "env")
	head := linkChain(t, maxLinkExpansions+6, realDir)
	linked := filepath.Join(head, "env")
	j := strictJudgement{pins: map[int]string{0: linked}}
	plan := &ExecPlan{Argv: []string{"env", "sh"}}
	err := pinKnownSlots(j, plan)
	require.Error(t, err)
	require.Contains(t, err.Error(), "link")
	require.Equal(t, "env", plan.Argv[0], "nothing is pinned when the directory is still a link")
}

// requireLaunchRefusalPrefix pins the launch-level wording that names the hook
// and the rule, which the bare pin error does not carry.
func requireLaunchRefusalPrefix(t *testing.T, err error) {
	t.Helper()
	require.ErrorContains(t, err, "exec launch refused for hook")
	require.ErrorContains(t, err, "strict program mode is on")
}

func requirePinRemedy(t *testing.T, err error, slot, program string) {
	t.Helper()
	require.ErrorContains(t, err, slot)
	require.ErrorContains(t, err, program)
	require.ErrorContains(t, err, "check the link")
	require.ErrorContains(t, err, "directory")
	require.NotContains(t, err.Error(), "approve")
	require.NotContains(t, err.Error(), "trust --program")
}

func TestPinKnownSlots_RefusalNamesSlotAndLinkRemedy(t *testing.T) {
	for _, tc := range []struct{ name, role string }{{"env", "env wrapper"}, {"sh", "interpreter"}} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			pin := filepath.Join(dir, "missing", tc.name)
			for _, arg := range []string{filepath.Join(dir, tc.name), tc.name} {
				plan := &ExecPlan{Argv: []string{arg}}
				err := pinKnownSlots(strictJudgement{pins: map[int]string{0: pin}}, plan)
				requirePinRemedy(t, err, tc.role, arg)
				require.Equal(t, arg, plan.Argv[0])
			}
		})
	}
}

func TestPhysicalDirectory_FailsClosedWhenItCannotBeResolved(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.True(t, physicalDirectory(base))
	require.False(t, physicalDirectory(filepath.Join(base, "missing")), "a directory that cannot be resolved is not physical")
	loop := filepath.Join(base, "loop")
	require.NoError(t, os.Symlink(loop, loop))
	require.False(t, physicalDirectory(loop), "a link loop is not physical")
}
