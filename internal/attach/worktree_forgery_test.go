// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forgedWorktree lays out a .git file, an admin directory at adminRel under
// base, and a commondir and back-link, all individually consistent, so a test
// can break exactly one guard.
func forgedWorktree(t *testing.T, adminRel, commondir string) (wtRoot string) {
	t.Helper()
	base := t.TempDir()
	wtRoot = filepath.Join(base, "wt")
	admin := filepath.Join(base, filepath.FromSlash(adminRel))
	require.NoError(t, os.MkdirAll(admin, 0o755))
	require.NoError(t, os.MkdirAll(wtRoot, 0o755))
	writeFile(t, filepath.Join(wtRoot, ".git"), "gitdir: "+admin+"\n")
	writeFile(t, filepath.Join(admin, "commondir"), commondir+"\n")
	writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(wtRoot, ".git")+"\n")
	return wtRoot
}

func TestWorktreeForgery_CommondirNamesAnotherRepository_IsInvalid(t *testing.T) {
	// The admin dir sits under main/.git/worktrees, but its commondir names a
	// different repository: a forged external admin dir must not borrow it.
	other := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(other, ".git"), 0o755))
	wt := forgedWorktree(t, "main/.git/worktrees/wt", filepath.Join(other, ".git"))
	got, err := DetectCheckout(wt)
	require.NoError(t, err)
	assert.Equal(t, CheckoutInvalid, got.Kind)
}

func TestWorktreeForgery_AdminDirOutsideWorktreesFolder_IsInvalid(t *testing.T) {
	// commondir and the back-link agree, but the admin dir is not under
	// <common>/worktrees, so it is not something git created.
	wt := forgedWorktree(t, "main/.git/elsewhere/wt", "../..")
	got, err := DetectCheckout(wt)
	require.NoError(t, err)
	assert.Equal(t, CheckoutInvalid, got.Kind)
}

func TestWorktreeForgery_ControlCase_AWellFormedLayoutIsAWorktree(t *testing.T) {
	wt := forgedWorktree(t, "main/.git/worktrees/wt", "../..")
	got, err := DetectCheckout(wt)
	require.NoError(t, err)
	assert.Equal(t, CheckoutWorktree, got.Kind, "the forgery fixtures differ from this layout by one guard each")
}

func TestWorktreeForgery_BareCommonDir_HasNoMainCheckout(t *testing.T) {
	wt := forgedWorktree(t, "repo.git/worktrees/wt", "../..")
	got, err := DetectCheckout(wt)
	require.NoError(t, err)
	assert.Equal(t, CheckoutInvalid, got.Kind)
}

func TestWorktreeForgery_GitFileWithoutGitdirPrefix_IsInvalid(t *testing.T) {
	wt := forgedWorktree(t, "main/.git/worktrees/wt", "../..")
	admin := readGitdirTarget(t, wt)
	writeFile(t, filepath.Join(wt, ".git"), admin+"\n") // a bare path, no gitdir: prefix
	got, err := DetectCheckout(wt)
	require.NoError(t, err)
	assert.Equal(t, CheckoutInvalid, got.Kind)
}

func TestWorktreeForgery_OversizedGitFileWithValidPrefix_IsInvalid(t *testing.T) {
	wt := forgedWorktree(t, "main/.git/worktrees/wt", "../..")
	admin := readGitdirTarget(t, wt)
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+admin+strings.Repeat(" ", maxGitFileBytes+10)+"\n")
	got, err := DetectCheckout(wt)
	require.NoError(t, err)
	assert.Equal(t, CheckoutInvalid, got.Kind, "a .git file past the size cap is refused, not truncated and trusted")
}

func TestWorktreeForgery_CommondirFileMissing_IsInvalid(t *testing.T) {
	wt := forgedWorktree(t, "main/.git/worktrees/wt", "../..")
	require.NoError(t, os.Remove(filepath.Join(readGitdirTarget(t, wt), "commondir")))
	got, err := DetectCheckout(wt)
	require.NoError(t, err)
	assert.Equal(t, CheckoutInvalid, got.Kind)
}

// commondir and the gitdir back-link are read as plain files: a symlink in
// their place could point at content elsewhere, so it is refused.
func TestWorktreeForgery_SymlinkedAdminFiles_AreInvalid(t *testing.T) {
	for _, name := range []string{"commondir", "gitdir"} {
		t.Run(name, func(t *testing.T) {
			wt := forgedWorktree(t, "main/.git/worktrees/wt", "../..")
			file := filepath.Join(readGitdirTarget(t, wt), name)
			body, err := os.ReadFile(file)
			require.NoError(t, err)
			target := filepath.Join(t.TempDir(), name+".real")
			writeFile(t, target, string(body)) // identical content, so only the symlink is wrong
			require.NoError(t, os.Remove(file))
			require.NoError(t, os.Symlink(target, file))
			got, err := DetectCheckout(wt)
			require.NoError(t, err)
			assert.Equal(t, CheckoutInvalid, got.Kind)
		})
	}
}

// A .git entry that cannot be inspected fails closed. It must not be skipped to
// find a checkout in a parent directory, which would judge the store by the
// wrong repository.
func TestDetectCheckout_UnreadableGitEntry_FailsClosedNotUpward(t *testing.T) {
	outer := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outer, ".git"), 0o755))
	inner := filepath.Join(outer, "inner")
	require.NoError(t, os.MkdirAll(filepath.Join(inner, ".git"), 0o755))
	require.NoError(t, os.Chmod(inner, 0o600)) // no search permission: inner/.git cannot be inspected
	t.Cleanup(func() { require.NoError(t, os.Chmod(inner, 0o755)) })
	if _, err := os.Lstat(filepath.Join(inner, ".git")); err == nil {
		t.Skip("running with permission to search the directory (root): the entry is readable")
	}
	got, err := DetectCheckout(inner)
	require.NoError(t, err)
	assert.Equal(t, CheckoutInvalid, got.Kind)
}
