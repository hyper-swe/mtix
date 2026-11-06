// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// declineRows runs the production doctor deps in dir and returns the rows.
func declineRows(t *testing.T, dir string) map[string]service.DoctorResult {
	t.Helper()
	t.Chdir(dir)
	deps, err := productionAttachDoctorDeps(context.Background())
	require.NoError(t, err)
	return attachDoctorRows(t, deps, false)
}

// A listing with no recorded identity over a store that has one is not a
// recreated store: registering from a normal shell records the identity, and
// the row says that, not "restore the store".
func TestDoctorGlue_ListingWithNoID_ShowsRegisterFixNotRestore(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	other := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(other, ".git"), 0o755))
	id := initStoreAt(t, other)
	require.NotEmpty(t, id)
	require.NoError(t, st.ListBoardInstance(t.Context(), oplocal.ListedBoard{Key: boardIDOne, Path: other, StoreInstanceID: ""}))

	row, ok := declineRows(t, other)["glue.store-identity"]
	require.True(t, ok)
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Contains(t, row.Detail, "records no store identity")
	assert.Contains(t, row.Fix, "mtix agent register")
	assert.NotContains(t, row.Fix, "restore")
}

// A store directory that is a symlink is not a recreated store either: the row
// names the symlink and tells the operator to make it a real directory.
func TestDoctorGlue_SymlinkedStoreDirectory_ShowsUnreadableRow(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	data := filepath.Join(root, ".mtix", "data")
	moved := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.Rename(data, moved))
	require.NoError(t, os.Symlink(moved, data))

	row, ok := declineRows(t, root)["glue.store-identity"]
	require.True(t, ok, "a listed board declined for its store path has a row")
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Contains(t, row.Detail, "cannot be read as a plain store")
	assert.Contains(t, row.Fix, "real directories")
	assert.NotContains(t, row.Detail, "recreated")
}

// A listed board whose .git entry stopped being a valid checkout (a pruned
// worktree, a moved main checkout, a junk file) is declined; the row says so
// and gives the repair step.
func TestDoctorGlue_ListedBoardWithInvalidCheckout_ShowsCheckoutRow(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	require.NoError(t, os.RemoveAll(filepath.Join(root, ".git")))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /nonexistent\n"), 0o600))

	row, ok := declineRows(t, root)["glue.checkout"]
	require.True(t, ok, "a listed board declined as no-checkout has a row")
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Contains(t, row.Detail, filepath.Join(root, ".git"))
	assert.Contains(t, row.Fix, "git worktree repair")
}

// A directory that is not listed and has a junk .git file gets no row: the
// glue stays quiet there.
func TestDoctorGlue_UnlistedJunkGit_HasNoCheckoutRow(t *testing.T) {
	saveAndResetApp(t)
	testutil.OperatorState(t)
	dir := t.TempDir()
	initStoreAt(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nonexistent\n"), 0o600))

	rows := declineRows(t, dir)
	assert.NotContains(t, rows, "glue.checkout")
	assert.NotContains(t, rows, "glue.store-identity")
}
