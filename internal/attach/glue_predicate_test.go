// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// predicateFixture is a plain checkout with a listed board and a store file.
type predicateFixture struct {
	root   string
	boards oplocal.DispatchBoards
	id     string
	idErr  error
	reads  []string
}

func newPredicateFixture(t *testing.T) *predicateFixture {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	writeFile(t, filepath.Join(root, ".mtix", "data", "mtix.db"), "db")
	return &predicateFixture{root: root, id: "inst-1",
		boards: oplocal.DispatchBoards{Listed: []oplocal.ListedBoard{{Key: "b-1", Path: root, StoreInstanceID: "inst-1"}}}}
}

func (f *predicateFixture) deps() PredicateDeps {
	return PredicateDeps{
		Boards: func(context.Context) (oplocal.DispatchBoards, error) { return f.boards, nil },
		StoreInstanceID: func(_ context.Context, dbPath string) (string, error) {
			f.reads = append(f.reads, dbPath)
			return f.id, f.idErr
		},
	}
}

func TestGluePredicate_ListedBoardMatchingInstance_Acts(t *testing.T) {
	f := newPredicateFixture(t)
	sub := filepath.Join(f.root, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	got := EvaluateGlue(context.Background(), f.deps(), sub)
	require.True(t, got.Act, got.Reason)
	assert.Equal(t, f.root, got.BoardRoot)
	assert.Equal(t, []string{filepath.Join(f.root, ".mtix", "data", "mtix.db")}, f.reads)
}

func TestGlue_UnlistedDir_PrintsNotAttachedOnly(t *testing.T) {
	f := newPredicateFixture(t)
	f.boards = oplocal.DispatchBoards{}
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	require.False(t, got.Act)
	assert.Equal(t, NotAttachedLine, got.Line)
	assert.Equal(t, "board not attached on this host", NotAttachedLine)
	assert.Equal(t, ReasonUnlisted, got.Reason)
	assert.Empty(t, f.reads, "an unlisted directory must not even open the store")
}

func TestGluePredicate_NoCheckout_NotAttached(t *testing.T) {
	f := newPredicateFixture(t)
	got := EvaluateGlue(context.Background(), f.deps(), t.TempDir())
	assert.False(t, got.Act)
	assert.Equal(t, ReasonNoCheckout, got.Reason)
}

func TestGluePredicate_ForgedInstanceID_NotAttached(t *testing.T) {
	f := newPredicateFixture(t)
	f.id = "some-other-store"
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	assert.False(t, got.Act)
	assert.Equal(t, ReasonIdentityMismatch, got.Reason)
}

func TestGluePredicate_EnrolledEntryWithDifferentInstance_NotAttached(t *testing.T) {
	f := newPredicateFixture(t)
	f.boards.Enrolled = []oplocal.EnrolledBoard{{Path: f.root, BoardID: "b-1", StoreInstanceID: "forged"}}
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	assert.False(t, got.Act, "an enrolled record that disagrees with the store is not a match")
	assert.Equal(t, ReasonIdentityMismatch, got.Reason)
}

func TestGluePredicate_EnrolledOnly_Acts(t *testing.T) {
	f := newPredicateFixture(t)
	f.boards.Listed = nil
	f.boards.Enrolled = []oplocal.EnrolledBoard{{Path: f.root, BoardID: "b-1", StoreInstanceID: "inst-1"}}
	assert.True(t, EvaluateGlue(context.Background(), f.deps(), f.root).Act)
}

func TestGluePredicate_FailureModes_FailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*predicateFixture, *PredicateDeps)
		reason string
	}{
		{"registry unreadable", func(_ *predicateFixture, d *PredicateDeps) {
			d.Boards = func(context.Context) (oplocal.DispatchBoards, error) {
				return oplocal.DispatchBoards{}, errors.New("no state")
			}
		}, ReasonStateUnavailable},
		{"store id unreadable", func(f *predicateFixture, _ *PredicateDeps) { f.idErr = errors.New("locked") }, ReasonIdentityMismatch},
		{"store file missing", func(f *predicateFixture, _ *PredicateDeps) {
			require.NoError(t, os.Remove(filepath.Join(f.root, ".mtix", "data", "mtix.db")))
		}, ReasonIdentityMismatch},
		{"no deps", func(_ *predicateFixture, d *PredicateDeps) { *d = PredicateDeps{} }, ReasonStateUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPredicateFixture(t)
			d := f.deps()
			tt.mutate(f, &d)
			got := EvaluateGlue(context.Background(), d, f.root)
			assert.False(t, got.Act)
			assert.Equal(t, tt.reason, got.Reason)
			assert.Equal(t, NotAttachedLine, got.Line)
		})
	}
}

func TestGluePredicate_CommittedStore_Ignored(t *testing.T) {
	f := newPredicateFixture(t)
	f.boards.Listed[0].StoreInstanceID = "" // a legacy listing carries no instance id
	f.id = ""
	writeIndex(t, filepath.Join(f.root, ".git"), buildIndex(2, []idxEntry{{path: ".mtix/data/mtix.db"}}))
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	require.False(t, got.Act)
	assert.Equal(t, ReasonTrackedStore, got.Reason)
	assert.Equal(t, NotAttachedLine, got.Line)
}

func TestGluePredicate_LegacyUntrackedStore_Acts(t *testing.T) {
	f := newPredicateFixture(t)
	f.boards.Listed[0].StoreInstanceID = ""
	f.id = ""
	writeIndex(t, filepath.Join(f.root, ".git"), buildIndex(2, []idxEntry{{path: "README.md"}}))
	assert.True(t, EvaluateGlue(context.Background(), f.deps(), f.root).Act)
}

func TestGluePredicate_LegacyUnsupportedIndex_FailsClosed(t *testing.T) {
	f := newPredicateFixture(t)
	f.boards.Listed[0].StoreInstanceID = ""
	f.id = ""
	writeIndex(t, filepath.Join(f.root, ".git"), buildIndex(2, []idxEntry{{path: "a"}}, ext("XYZW", nil)))
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	assert.False(t, got.Act)
	assert.Equal(t, ReasonIndexUnsupported, got.Reason)
}

func TestGluePredicate_IdentifiedStore_SkipsIndexReader(t *testing.T) {
	f := newPredicateFixture(t)
	// A corrupt index must not matter once the instance id matches the record.
	writeIndex(t, filepath.Join(f.root, ".git"), []byte("garbage"))
	assert.True(t, EvaluateGlue(context.Background(), f.deps(), f.root).Act)
}

func TestGluePredicate_ListedWorktreeWithoutBacklink_NotAttached(t *testing.T) {
	_, wtRoot := linkedWorktree(t)
	require.NoError(t, os.Remove(filepath.Join(readGitdirTarget(t, wtRoot), "gitdir")))
	writeFile(t, filepath.Join(wtRoot, ".mtix", "data", "mtix.db"), "db")
	f := &predicateFixture{root: wtRoot, id: "inst-1",
		boards: oplocal.DispatchBoards{Listed: []oplocal.ListedBoard{{Key: "b-1", Path: wtRoot, StoreInstanceID: "inst-1"}}}}
	got := EvaluateGlue(context.Background(), f.deps(), wtRoot)
	assert.False(t, got.Act, "a forged commondir must not stand in for a checkout")
	assert.Equal(t, ReasonNoCheckout, got.Reason)
	assert.Equal(t, wtRoot, got.StoreRoot)
}

func TestGluePredicate_ListedWorktreeOwnRecord_ActsOnItsOwnStore(t *testing.T) {
	mainRoot, wtRoot := linkedWorktree(t)
	writeFile(t, filepath.Join(mainRoot, ".mtix", "data", "mtix.db"), "db")
	writeFile(t, filepath.Join(wtRoot, ".mtix", "data", "mtix.db"), "db")
	f := &predicateFixture{root: wtRoot, id: "wt-inst",
		boards: oplocal.DispatchBoards{Listed: []oplocal.ListedBoard{
			{Key: "main-key", Path: mainRoot, StoreInstanceID: "main-inst"},
			{Key: "wt-key", Path: wtRoot, StoreInstanceID: "wt-inst"},
		}}}
	got := EvaluateGlue(context.Background(), f.deps(), wtRoot)
	require.True(t, got.Act, got.Reason)
	assert.Equal(t, wtRoot, got.BoardRoot, "the worktree's own listing and store win over the main checkout")
	assert.Equal(t, "wt-key", got.BoardKey)
	assert.Equal(t, []string{filepath.Join(wtRoot, ".mtix", "data", "mtix.db")}, f.reads)
}

func TestGluePredicate_UnlistedWorktreeOfUnlistedMain_NotAttached(t *testing.T) {
	_, wtRoot := linkedWorktree(t)
	writeFile(t, filepath.Join(wtRoot, ".mtix", "data", "mtix.db"), "db")
	f := &predicateFixture{root: wtRoot, id: "x"}
	got := EvaluateGlue(context.Background(), f.deps(), wtRoot)
	assert.False(t, got.Act)
	assert.Equal(t, ReasonUnlisted, got.Reason)
}

func TestGluePredicate_ListedBoardWithoutGit_Acts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".mtix", "data", "mtix.db"), "db")
	sub := filepath.Join(root, "work", "deep")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	f := &predicateFixture{root: root, id: "inst-1",
		boards: oplocal.DispatchBoards{Listed: []oplocal.ListedBoard{{Key: "b", Path: root, StoreInstanceID: "inst-1"}}}}
	got := EvaluateGlue(context.Background(), f.deps(), sub)
	require.True(t, got.Act, got.Reason)
	assert.Equal(t, root, got.BoardRoot)
}

func TestGluePredicate_LegacyBoardWithoutGit_ActsWithoutIndexCheck(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".mtix", "data", "mtix.db"), "db")
	f := &predicateFixture{root: root, id: "",
		boards: oplocal.DispatchBoards{Listed: []oplocal.ListedBoard{{Key: "b", Path: root}}}}
	assert.True(t, EvaluateGlue(context.Background(), f.deps(), root).Act)
}

func TestGluePredicate_UnlistedDirWithoutGitOrMtix_NoCheckout(t *testing.T) {
	f := newPredicateFixture(t)
	got := EvaluateGlue(context.Background(), f.deps(), t.TempDir())
	assert.Equal(t, ReasonNoCheckout, got.Reason)
}

func TestGluePredicate_SymlinkedWorkingDirectory_Acts(t *testing.T) {
	f := newPredicateFixture(t)
	link := filepath.Join(t.TempDir(), "via-link")
	require.NoError(t, os.Symlink(f.root, link))
	got := EvaluateGlue(context.Background(), f.deps(), link)
	require.True(t, got.Act, got.Reason)
	assert.Equal(t, f.root, got.BoardRoot)
}

func TestGluePredicate_StorePathThroughSymlink_NotAttached(t *testing.T) {
	f := newPredicateFixture(t)
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "data", "mtix.db"), "db")
	require.NoError(t, os.RemoveAll(filepath.Join(f.root, ".mtix")))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(f.root, ".mtix")))
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	assert.False(t, got.Act, "a committed symlink must not lend another store")
	assert.Equal(t, ReasonIdentityMismatch, got.Reason)
}

func TestGluePredicate_LegacyTrackedSymlinkAncestor_CarriesCause(t *testing.T) {
	f := newPredicateFixture(t)
	f.boards.Listed[0].StoreInstanceID = ""
	f.id = ""
	writeIndex(t, filepath.Join(f.root, ".git"), buildIndex(2, []idxEntry{{path: ".mtix/data", mode: 0o120000}}))
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	assert.False(t, got.Act)
	assert.Equal(t, ReasonTrackedStore, got.Reason)
	assert.Equal(t, TrackedAncestor, got.Cause)
}

func TestGluePredicate_LegacyUnsupportedIndex_CarriesCause(t *testing.T) {
	f := newPredicateFixture(t)
	f.boards.Listed[0].StoreInstanceID = ""
	f.id = ""
	writeIndex(t, filepath.Join(f.root, ".git"), buildIndex(2, nil, ext("XYZW", nil)))
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	assert.Equal(t, ReasonIndexUnsupported, got.Reason)
	assert.Equal(t, CauseExtension, got.Cause)
}
