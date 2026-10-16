// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func instructionCoverageRoot(t *testing.T, tracked string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "index"), approvalIndexFixture(2, tracked, ""), 0600))
	return root
}

func TestInstructionAgents_NestedTrackedSkillsIgnoreSymlinksAndOtherFiles(t *testing.T) {
	for _, dir := range []string{".claude-plugin/skills", ".codex-plugin/skills", ".agents/skills"} {
		t.Run(dir, func(t *testing.T) {
			name := dir + "/fixture/SKILL.md"
			root := instructionCoverageRoot(t, name)
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("mtix agent register nested-agent\n"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(root, dir, "ignored.txt"), []byte("mtix agent register ignored-agent\n"), 0600))
			outside := filepath.Join(t.TempDir(), "outside.md")
			require.NoError(t, os.WriteFile(outside, []byte("mtix agent register outside-agent\n"), 0600))
			require.NoError(t, os.Symlink(outside, filepath.Join(root, dir, "link.md")))
			require.NoError(t, os.Symlink(filepath.Dir(outside), filepath.Join(root, dir, "linked-directory")))
			ids, err := InstructionAgents(root, nil)
			require.NoError(t, err)
			require.Equal(t, []string{"nested-agent"}, ids)
		})
	}
}

func TestInstructionAgents_FileCountLimitReturnsNoPartialNames(t *testing.T) {
	root := instructionCoverageRoot(t, ".agents/skills/000.md")
	dir := filepath.Join(root, ".agents", "skills")
	require.NoError(t, os.MkdirAll(dir, 0700))
	for i := range 257 {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d.md", i)), []byte("mtix agent register fixture-agent\n"), 0600))
	}
	ids, err := InstructionAgents(root, nil)
	require.ErrorContains(t, err, "instruction file limit exceeded")
	require.Empty(t, ids)
}

func TestInstructionAgents_OversizedTrackedInputReturnsNoPartialNames(t *testing.T) {
	root := instructionCoverageRoot(t, "AGENTS.md")
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(strings.Repeat("x", (1<<20)+1)), 0600))
	ids, err := InstructionAgents(root, nil)
	require.ErrorContains(t, err, "instruction input exceeds limit")
	require.Empty(t, ids)
}

func TestInstructionRead_ActualRootRejectsMissingClosedAndNonregularInputs(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	_, err = instructionRead(root, "missing", 32)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, root.Mkdir("directory", 0700))
	_, err = instructionRead(root, "directory", 32)
	require.ErrorContains(t, err, "not a regular file")
	closed, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, closed.Close())
	_, err = instructionRead(closed, "missing", 32)
	require.Error(t, err)
}
