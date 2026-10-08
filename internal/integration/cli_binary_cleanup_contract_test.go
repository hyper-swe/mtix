// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Cleanup is confined to one owned parent, with complete validation before writes.
package integration

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupBuiltBinary_OwnedParent_RemovesOnlyCompleteInventory(t *testing.T) {
	cases := []struct {
		name           string
		binary, nested bool
	}{{"built", true, true}, {"failed build", false, false}, {"empty nested directory", false, true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			temp, root, sibling := binaryCleanupFixture(t)
			t.Setenv("TMPDIR", temp)
			binary := filepath.Join(root, "mtix")
			if tc.binary {
				require.NoError(t, os.WriteFile(binary, []byte("binary"), 0600))
			}
			if tc.nested {
				nested := filepath.Join(root, "nested", "deeper")
				require.NoError(t, os.MkdirAll(nested, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(nested, "artifact"), []byte("artifact"), 0600))
			}
			require.NoError(t, cleanupBuiltBinary(binary))
			assert.NoDirExists(t, root)
			require.NoError(t, cleanupBuiltBinary(binary), "already missing owned parent is harmless")
			content, err := os.ReadFile(sibling)
			require.NoError(t, err)
			assert.Equal(t, "sibling", string(content))
		})
	}
	require.NoError(t, cleanupBuiltBinary(""), "no build created a parent")
}

func binaryCleanupFixture(t *testing.T) (string, string, string) {
	t.Helper()
	sandbox := t.TempDir()
	temp := filepath.Join(sandbox, "temp")
	require.NoError(t, os.Mkdir(temp, 0700))
	root, err := os.MkdirTemp(temp, "mtix-cli-test-")
	require.NoError(t, err)
	sibling := filepath.Join(temp, "mtix-cli-test-sibling")
	require.NoError(t, os.Mkdir(sibling, 0700))
	sentinel := filepath.Join(sibling, "sentinel")
	require.NoError(t, os.WriteFile(sentinel, []byte("sibling"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a-sentinel"), []byte("owned"), 0600))
	return temp, root, sentinel
}

func TestCleanupBuiltBinary_UnsafePaths_RefusesBeforeAnyDeletion(t *testing.T) {
	cases := []string{"outside", "temp root", "wrong prefix", "empty suffix", "wrong binary", "relative", "traversal", "symlink root", "symlink child", "symlink nested directory", "fifo"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			temp, root, sibling := binaryCleanupFixture(t)
			t.Setenv("TMPDIR", temp)
			binary := unsafeBinaryFixture(t, name, temp, root, sibling)
			require.Error(t, cleanupBuiltBinary(binary))
			owned, err := os.ReadFile(filepath.Join(root, "a-sentinel"))
			require.NoError(t, err, "inventory must be completely validated before removal")
			assert.Equal(t, "owned", string(owned))
			other, err := os.ReadFile(sibling)
			require.NoError(t, err)
			assert.Equal(t, "sibling", string(other))
		})
	}
}

func unsafeBinaryFixture(t *testing.T, name, temp, root, sibling string) string {
	t.Helper()
	binary := filepath.Join(root, "mtix")
	switch name {
	case "outside":
		return filepath.Join(filepath.Dir(temp), "mtix-cli-test-outside", "mtix")
	case "temp root":
		return filepath.Join(temp, "mtix")
	case "wrong prefix":
		return filepath.Join(temp, "other", "mtix")
	case "empty suffix":
		return filepath.Join(temp, "mtix-cli-test-", "mtix")
	case "wrong binary":
		return filepath.Join(root, "other")
	case "relative":
		return "mtix-cli-test-relative/mtix"
	case "traversal":
		return root + "/../mtix-cli-test-sibling/mtix"
	case "symlink root":
		link := filepath.Join(temp, "mtix-cli-test-link")
		require.NoError(t, os.Symlink(root, link))
		return filepath.Join(link, "mtix")
	case "symlink child":
		require.NoError(t, os.Symlink(sibling, filepath.Join(root, "z-link")))
	case "symlink nested directory":
		require.NoError(t, os.Symlink(filepath.Dir(sibling), filepath.Join(root, "z-directory")))
	case "fifo":
		require.NoError(t, syscall.Mkfifo(filepath.Join(root, "z-fifo"), 0600))
	}
	return binary
}
