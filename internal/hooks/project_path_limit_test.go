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

func projectLinkChain(t *testing.T, n int, target string) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for i := n - 1; i >= 0; i-- {
		next := target
		if i < n-1 {
			next = fmt.Sprintf("l%d", i+1)
		}
		require.NoError(t, os.Symlink(next, filepath.Join(base, fmt.Sprintf("l%d", i))))
	}
	return filepath.Join(base, "l0")
}

func TestProjectPath_LinkLimitFailsClosed(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for _, n := range []int{40, maxLinkExpansions, maxLinkExpansions + 1, maxLinkExpansions + 256} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			head := projectLinkChain(t, n, root)
			resolved, lexical, inside := projectPath(root, root, head)
			if n > maxLinkExpansions {
				require.Empty(t, resolved, "an exhausted resolution has no physical answer")
			} else {
				require.Equal(t, root, resolved, "a chain within the budget resolves completely to its target")
			}
			require.False(t, lexical)
			require.True(t, inside, "an unresolved path must never be classified as outside")
			in := &hookInputs{logical: root, root: root, program: 0, script: -1}
			require.Error(t, checkOperands(Hook{Name: "wake", Exec: &ExecConfig{Command: []string{head}}}, in))
			require.Error(t, checkEnvAssignments([]string{"env", "HOME=" + head, "cat"}, 2, root, root))
			_, launchErr := launchProgram("wake", in, head)
			require.Error(t, launchErr)
		})
	}
}

func TestPrepareExec_LinkLimitRefusedInEveryMode(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, n := range []int{maxLinkExpansions + 1, maxLinkExpansions + 256} {
			t.Run(fmt.Sprintf("strict=%t/links=%d", strict, n), func(t *testing.T) {
				f := newPrepareFixture(t, "[sh, scripts/run.sh]")
				if strict {
					require.NoError(t, f.approvals.SetStrict(true, true))
				}
				head := projectLinkChain(t, n, f.root)
				config := "hooks:\n  - name: wake\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [sh, scripts/run.sh, " + head + "]}\n"
				require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "hooks.yaml"), []byte(config), 0o600))
				require.NoError(t, f.approvals.Approve("wake", true))
				_, err := f.approvals.PrepareExec(f.hook(t))
				require.Error(t, err)
			})
		}
	}
}

func TestResolveLinks_UnreadableLink_IsIncomplete(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(base, "l")
	require.NoError(t, os.Symlink(base, link))
	failing := func(string) (string, error) { return "", os.ErrPermission }
	_, complete := resolveLinks(link, 0, failing)
	require.False(t, complete, "a link that cannot be read is never a complete resolution")
	_, complete = resolveLinks(link, 0, os.Readlink)
	require.True(t, complete)
}

func TestLaunchProgram_LinkLimit_NamesTheLimitNotAnEmptyTarget(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	head := projectLinkChain(t, maxLinkExpansions+1, root)
	in := &hookInputs{logical: root, root: root, program: 0, script: -1}
	_, err = launchProgram("wake", in, head)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds the link expansion limit")
	require.NotContains(t, err.Error(), `resolves to ""`)
}

// A chain of 40 links is far below every operating system's own limit and must
// resolve completely whatever the budget constant is set to.
func TestProjectPath_FortyLinkChain_ResolvesCompletelyToItsTarget(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	resolved, complete := resolveDeepestStatus(projectLinkChain(t, 40, root), 0)
	require.True(t, complete)
	require.Equal(t, root, resolved)
}
