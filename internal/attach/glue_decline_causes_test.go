// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// A listed board that is declined says why, so doctor can print an honest row
// and the right recovery for each case (MTIX-112.19.8, ADR-007 A5).
func TestGluePredicate_ListedBoardDeclines_CarryTheirCause(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, f *predicateFixture)
		reason string
		cause  string
	}{
		{"both ids present and different", func(_ *testing.T, f *predicateFixture) { f.id = "inst-2" }, ReasonIdentityMismatch, CauseStoreChanged},
		{"listing records no id for an identified store", func(_ *testing.T, f *predicateFixture) {
			f.boards.Listed[0].StoreInstanceID = ""
		}, ReasonIdentityMismatch, CauseListingNoID},
		{"store id unreadable", func(_ *testing.T, f *predicateFixture) { f.idErr = os.ErrPermission }, ReasonIdentityMismatch, CauseStoreUnreadable},
		{"store directory is a symlink", func(t *testing.T, f *predicateFixture) {
			elsewhere := t.TempDir()
			writeFile(t, filepath.Join(elsewhere, "data", "mtix.db"), "db")
			require.NoError(t, os.RemoveAll(filepath.Join(f.root, ".mtix")))
			require.NoError(t, os.Symlink(elsewhere, filepath.Join(f.root, ".mtix")))
		}, ReasonIdentityMismatch, CauseStoreUnreadable},
		{"data directory is a symlink", func(t *testing.T, f *predicateFixture) {
			elsewhere := t.TempDir()
			writeFile(t, filepath.Join(elsewhere, "mtix.db"), "db")
			require.NoError(t, os.RemoveAll(filepath.Join(f.root, ".mtix", "data")))
			require.NoError(t, os.Symlink(elsewhere, filepath.Join(f.root, ".mtix", "data")))
		}, ReasonIdentityMismatch, CauseStoreUnreadable},
		{"junk .git file over a listed board", func(t *testing.T, f *predicateFixture) {
			require.NoError(t, os.RemoveAll(filepath.Join(f.root, ".git")))
			writeFile(t, filepath.Join(f.root, ".git"), "gitdir: /nonexistent\n")
		}, ReasonNoCheckout, CauseListedCheckout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPredicateFixture(t)
			tt.mutate(t, f)
			got := EvaluateGlue(context.Background(), f.deps(), f.root)
			assert.False(t, got.Act)
			assert.Equal(t, tt.reason, got.Reason)
			assert.Equal(t, tt.cause, got.Cause)
			assert.Equal(t, f.root, got.StoreRoot)
		})
	}
}

// An empty listing id over a legacy store that also has no id is not a mismatch.
func TestGluePredicate_EmptyListingIDOverLegacyStore_Acts(t *testing.T) {
	f := newPredicateFixture(t)
	f.id = ""
	f.boards.Listed[0].StoreInstanceID = ""
	assert.True(t, EvaluateGlue(context.Background(), f.deps(), f.root).Act)
}

// A directory that is not listed gets no cause and no row, whatever its .git is.
func TestGluePredicate_UnlistedJunkGit_HasNoCause(t *testing.T) {
	f := newPredicateFixture(t)
	require.NoError(t, os.RemoveAll(filepath.Join(f.root, ".git")))
	writeFile(t, filepath.Join(f.root, ".git"), "gitdir: /nonexistent\n")
	f.boards = oplocal.DispatchBoards{}
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	assert.Equal(t, ReasonNoCheckout, got.Reason)
	assert.Empty(t, got.Cause)
}

// A registry that cannot be read leaves the no-checkout decline as it was.
func TestGluePredicate_JunkGitWithUnreadableRegistry_StaysNoCheckoutWithoutCause(t *testing.T) {
	f := newPredicateFixture(t)
	require.NoError(t, os.RemoveAll(filepath.Join(f.root, ".git")))
	writeFile(t, filepath.Join(f.root, ".git"), "gitdir: /nonexistent\n")
	d := f.deps()
	d.Boards = func(context.Context) (oplocal.DispatchBoards, error) {
		return oplocal.DispatchBoards{}, os.ErrPermission
	}
	got := EvaluateGlue(context.Background(), d, f.root)
	assert.Equal(t, ReasonNoCheckout, got.Reason)
	assert.Empty(t, got.Cause)
}
