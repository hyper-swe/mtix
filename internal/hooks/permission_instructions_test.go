// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstructionAgents_GitTrackedNames_ExactBoundariesAndNoDefaultSelection(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "index"), approvalIndexFixture(2, "AGENTS.md", ""), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("mtix agent register named-only\nexisting-agent, existing-agent-extra\nmtix agent register --harness ignored\nmtix agent register <placeholder>\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("mtix agent register untracked-agent"), 0600))
	ids, err := InstructionAgents(root, []string{"existing-agent", "existing", "not-mentioned"})
	require.NoError(t, err)
	assert.Equal(t, []string{"existing-agent", "named-only"}, ids)
}

func TestInstructionAgents_NonGitOrUntracked_NoInstructionFact(t *testing.T) {
	for _, git := range []bool{false, true} {
		t.Run(map[bool]string{false: "non_git", true: "untracked"}[git], func(t *testing.T) {
			root := t.TempDir()
			if git {
				require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0700))
				require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "index"), approvalIndexFixture(2, "unrelated.md", ""), 0600))
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("mtix agent register fixture-agent"), 0600))
			ids, err := InstructionAgents(root, []string{"fixture-agent"})
			require.NoError(t, err)
			assert.Empty(t, ids)
		})
	}
}

func TestInstructionAgents_MalformedGitIndex_Refuses(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "index"), []byte("invalid synthetic index"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("mtix agent register fixture-agent"), 0600))
	_, err := InstructionAgents(root, nil)
	require.Error(t, err)
}

func TestInstructionAgents_VerifiedWorktreePointer_UsesLinkedIndex(t *testing.T) {
	root, git := t.TempDir(), t.TempDir()
	pointer := filepath.Join(root, ".git")
	require.NoError(t, os.WriteFile(pointer, []byte("gitdir: "+git+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(git, "gitdir"), []byte(pointer+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(git, "index"), approvalIndexFixture(4, "AGENTS.md", ""), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("mtix agent register fixture-agent"), 0600))
	ids, err := InstructionAgents(root, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"fixture-agent"}, ids)
	require.NoError(t, os.WriteFile(filepath.Join(git, "gitdir"), []byte(filepath.Join(t.TempDir(), ".git")), 0600))
	_, err = InstructionAgents(root, nil)
	require.Error(t, err)
}
