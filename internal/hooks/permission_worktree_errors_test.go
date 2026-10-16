// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func instructionRelativeWorktree(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	root, git := filepath.Join(base, "checkout"), filepath.Join(base, "git")
	require.NoError(t, os.Mkdir(root, 0700))
	require.NoError(t, os.Mkdir(git, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: ../git\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(git, "gitdir"), []byte("../checkout/.git\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(git, "index"), approvalIndexFixture(2, "AGENTS.md", ""), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("mtix agent register fixture-agent\n"), 0600))
	return root, git
}

func TestInstructionAgents_RelativeWorktreeMetadataAndAbsentIndex(t *testing.T) {
	root, git := instructionRelativeWorktree(t)
	ids, err := InstructionAgents(root, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"fixture-agent"}, ids)
	require.NoError(t, os.Remove(filepath.Join(git, "index")))
	ids, err = InstructionAgents(root, nil)
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestInstructionAgents_UnverifiedWorktreeAndIndexNeverReturnNames(t *testing.T) {
	cases := []string{"pointer_prefix", "pointer_newline", "pointer_oversized", "target_missing", "backlink_missing", "backlink_wrong", "backlink_directory", "index_directory", "index_oversized"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			root, git := instructionRelativeWorktree(t)
			breakInstructionWorktree(t, root, git, name)
			ids, err := InstructionAgents(root, nil)
			require.Error(t, err)
			require.Empty(t, ids, "unverified input must not produce partial instruction facts")
		})
	}
}

func breakInstructionWorktree(t *testing.T, root, git, name string) {
	t.Helper()
	pointer, backlink, index := filepath.Join(root, ".git"), filepath.Join(git, "gitdir"), filepath.Join(git, "index")
	write := func(path, text string) { require.NoError(t, os.WriteFile(path, []byte(text), 0600)) }
	switch name {
	case "pointer_prefix":
		write(pointer, "unverified: ../git")
	case "pointer_newline":
		write(pointer, "gitdir: ../git\nsecond target")
	case "pointer_oversized":
		write(pointer, strings.Repeat("x", 4097))
	case "target_missing":
		write(pointer, "gitdir: ../missing")
	case "backlink_missing":
		require.NoError(t, os.Remove(backlink))
	case "backlink_wrong":
		write(backlink, "../checkout/AGENTS.md")
	case "backlink_directory":
		require.NoError(t, os.Remove(backlink))
		require.NoError(t, os.Mkdir(backlink, 0700))
	case "index_directory":
		require.NoError(t, os.Remove(index))
		require.NoError(t, os.Mkdir(index, 0700))
	case "index_oversized":
		write(index, strings.Repeat("x", int(maxApprovalIndex)+1))
	default:
		t.Fatalf("unknown synthetic worktree case %q", name)
	}
}
