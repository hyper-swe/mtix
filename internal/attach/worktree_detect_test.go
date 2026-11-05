// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFile creates dir and writes name under it for a worktree fixture.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// linkedWorktree builds a main checkout and a linked worktree fixture the way
// git lays them out, and returns both roots.
func linkedWorktree(t *testing.T) (mainRoot, wtRoot string) {
	t.Helper()
	base := t.TempDir()
	mainRoot = filepath.Join(base, "main")
	wtRoot = filepath.Join(base, "wt")
	admin := filepath.Join(mainRoot, ".git", "worktrees", "wt")
	require.NoError(t, os.MkdirAll(admin, 0o755))
	require.NoError(t, os.MkdirAll(wtRoot, 0o755))
	writeFile(t, filepath.Join(wtRoot, ".git"), "gitdir: "+admin+"\n")
	writeFile(t, filepath.Join(admin, "commondir"), "../..\n")
	writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(wtRoot, ".git")+"\n")
	return mainRoot, wtRoot
}

func TestDetectCheckout_PlainRepo_ReturnsPlain(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	sub := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	got, err := DetectCheckout(sub)
	require.NoError(t, err)
	assert.Equal(t, CheckoutPlain, got.Kind)
	assert.Equal(t, root, got.Root)
	assert.Equal(t, filepath.Join(root, ".git"), got.GitDir)
}

func TestDetectCheckout_LinkedWorktreeWithBacklink_ReturnsWorktree(t *testing.T) {
	mainRoot, wtRoot := linkedWorktree(t)

	got, err := DetectCheckout(wtRoot)
	require.NoError(t, err)
	assert.Equal(t, CheckoutWorktree, got.Kind)
	assert.Equal(t, wtRoot, got.Root)
	assert.Equal(t, mainRoot, got.CommonRoot)
}

func TestWorktreeDetect_CommondirWithoutBacklink_NotWorktree(t *testing.T) {
	_, wtRoot := linkedWorktree(t)
	admin := readGitdirTarget(t, wtRoot)
	require.NoError(t, os.Remove(filepath.Join(admin, "gitdir")))

	got, err := DetectCheckout(wtRoot)
	require.NoError(t, err)
	assert.Equal(t, CheckoutNone, got.Kind, "commondir without the back-link is forgeable")
}

func TestWorktreeDetect_BacklinkPointsElsewhere_NotWorktree(t *testing.T) {
	_, wtRoot := linkedWorktree(t)
	admin := readGitdirTarget(t, wtRoot)
	writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(t.TempDir(), ".git")+"\n")

	got, err := DetectCheckout(wtRoot)
	require.NoError(t, err)
	assert.Equal(t, CheckoutNone, got.Kind)
}

func TestWorktreeDetect_GitFilePointingAtCommonDir_NotWorktree(t *testing.T) {
	base := t.TempDir()
	common := filepath.Join(base, "main", ".git")
	require.NoError(t, os.MkdirAll(common, 0o755))
	wt := filepath.Join(base, "wt")
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+common+"\n")

	got, err := DetectCheckout(wt)
	require.NoError(t, err)
	assert.Equal(t, CheckoutNone, got.Kind, "a .git file naming the common dir itself has no admin dir")
}

func TestDetectCheckout_MalformedGitFile_NotWorktree(t *testing.T) {
	tests := map[string]string{
		"no prefix":     "not a gitdir line\n",
		"empty target":  "gitdir:\n",
		"oversized":     "gitdir: " + string(make([]byte, 8192)),
		"missing admin": "gitdir: /nonexistent/for/this/test\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, ".git"), body)
			got, err := DetectCheckout(root)
			require.NoError(t, err)
			assert.Equal(t, CheckoutNone, got.Kind)
		})
	}
}

func TestDetectCheckout_NoGitAnywhere_ReturnsNone(t *testing.T) {
	got, err := DetectCheckout(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, CheckoutNone, got.Kind)
}

func readGitdirTarget(t *testing.T, wtRoot string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(wtRoot, ".git"))
	require.NoError(t, err)
	return filepath.Clean(string(raw[len("gitdir: ") : len(raw)-1]))
}
