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
