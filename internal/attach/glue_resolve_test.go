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

// listRoots returns a fixture whose registry lists exactly the given roots
// with the instance id the fixture's store reads back.
func listRoots(f *predicateFixture, roots ...string) {
	f.boards = oplocal.DispatchBoards{}
	for i, r := range roots {
		f.boards.Listed = append(f.boards.Listed, oplocal.ListedBoard{Key: "key-" + filepath.Base(r) + string(rune('a'+i)), Path: r, StoreInstanceID: f.id})
	}
}

func dbOf(root string) string { return filepath.Join(root, ".mtix", "data", "mtix.db") }

// The store the verb would open is the nearest .mtix from the physical cwd;
// the predicate judges exactly that store (MTIX-112.19.8 ruling r3, points 1-3).
func TestGluePredicate_NestedStoreUnderListedRoot_JudgedAsItselfAndDeclined(t *testing.T) {
	f := newPredicateFixture(t) // f.root is listed and holds a store
	nested := filepath.Join(f.root, "vend", "x")
	writeFile(t, dbOf(nested), "db")
	for _, cwd := range []string{nested, filepath.Join(nested, "src", "deep")} {
		require.NoError(t, os.MkdirAll(cwd, 0o755))
		f.reads = nil
		got := EvaluateGlue(context.Background(), f.deps(), cwd)
		assert.False(t, got.Act, "a committed nested store is not the listed root's store (%s)", cwd)
		assert.Equal(t, ReasonUnlisted, got.Reason)
		assert.Equal(t, NotAttachedLine, got.Line)
		assert.Empty(t, f.reads, "an unlisted store is never opened")
	}
}

func TestGluePredicate_NestedStoreListedItself_ActsOnThatStoreOnly(t *testing.T) {
	f := newPredicateFixture(t)
	nested := filepath.Join(f.root, "vend", "x")
	writeFile(t, dbOf(nested), "db")
	listRoots(f, f.root, nested)
	got := EvaluateGlue(context.Background(), f.deps(), nested)
	require.True(t, got.Act, got.Reason)
	assert.Equal(t, nested, got.BoardRoot)
	assert.Equal(t, []string{dbOf(nested)}, f.reads)
}

func TestGluePredicate_SubdirectoryBoardOfGitCheckout_ActsWhenItsOwnStoreIsListed(t *testing.T) {
	f := newPredicateFixture(t)
	board := filepath.Join(f.root, "pkg")
	writeFile(t, dbOf(board), "db")
	require.NoError(t, os.RemoveAll(filepath.Join(f.root, ".mtix")))
	listRoots(f, board)
	require.NoError(t, os.MkdirAll(filepath.Join(board, "src"), 0o755))
	got := EvaluateGlue(context.Background(), f.deps(), filepath.Join(board, "src"))
	require.True(t, got.Act, got.Reason)
	assert.Equal(t, board, got.BoardRoot)
	assert.Equal(t, []string{dbOf(board)}, f.reads)
}

func TestGluePredicate_SubdirectoryBoardLegacy_IndexJudgedByCheckoutRelativePath(t *testing.T) {
	tests := []struct {
		name    string
		tracked string
		want    bool
		reason  string
	}{
		{"store of the board is tracked", "pkg/.mtix/data/mtix.db", false, ReasonTrackedStore},
		{"a root-level store path is not this board's", ".mtix/data/mtix.db", true, ReasonAttached},
		{"another package's store is not this board's", "other/.mtix/data/mtix.db", true, ReasonAttached},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPredicateFixture(t)
			board := filepath.Join(f.root, "pkg")
			writeFile(t, dbOf(board), "db")
			require.NoError(t, os.RemoveAll(filepath.Join(f.root, ".mtix")))
			f.id = ""
			listRoots(f, board)
			writeIndex(t, filepath.Join(f.root, ".git"), buildIndex(2, []idxEntry{{path: tt.tracked}}))
			got := EvaluateGlue(context.Background(), f.deps(), board)
			assert.Equal(t, tt.want, got.Act)
			assert.Equal(t, tt.reason, got.Reason)
		})
	}
}

// nestedWorktree is a linked worktree whose directory sits inside its main
// checkout, so a worktree without a store of its own resolves to main's.
func nestedWorktree(t *testing.T) (mainRoot, wtRoot string) {
	t.Helper()
	mainRoot = t.TempDir()
	wtRoot = filepath.Join(mainRoot, "wt")
	admin := filepath.Join(mainRoot, ".git", "worktrees", "wt")
	require.NoError(t, os.MkdirAll(admin, 0o755))
	require.NoError(t, os.MkdirAll(wtRoot, 0o755))
	writeFile(t, filepath.Join(wtRoot, ".git"), "gitdir: "+admin+"\n")
	writeFile(t, filepath.Join(admin, "commondir"), "../..\n")
	writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(wtRoot, ".git")+"\n")
	return mainRoot, wtRoot
}

func TestGluePredicate_UnlistedWorktreeWithOwnStore_DeclinedEvenWhenMainIsListed(t *testing.T) {
	mainRoot, wtRoot := linkedWorktree(t)
	writeFile(t, dbOf(mainRoot), "db")
	writeFile(t, dbOf(wtRoot), "db") // a store committed on the worktree's branch
	f := &predicateFixture{root: mainRoot, id: "inst-1"}
	listRoots(f, mainRoot)
	got := EvaluateGlue(context.Background(), f.deps(), wtRoot)
	assert.False(t, got.Act, "the worktree's own store is judged as itself")
	assert.Equal(t, ReasonUnlisted, got.Reason)
	assert.Empty(t, f.reads)
}

func TestGluePredicate_WorktreeNestedInListedMainWithoutOwnStore_JudgedAsMainsStore(t *testing.T) {
	mainRoot, wtRoot := nestedWorktree(t)
	writeFile(t, dbOf(mainRoot), "db")
	f := &predicateFixture{root: mainRoot, id: "inst-1"}
	listRoots(f, mainRoot)
	got := EvaluateGlue(context.Background(), f.deps(), wtRoot)
	require.True(t, got.Act, got.Reason)
	assert.Equal(t, mainRoot, got.BoardRoot, "the store the verb would open is main's, and main is listed")
}

func TestGluePredicate_ListedLegacyWorktree_IndexIsTheWorktreesOwn(t *testing.T) {
	mainRoot, wtRoot := linkedWorktree(t)
	writeFile(t, dbOf(wtRoot), "db")
	f := &predicateFixture{root: wtRoot, id: ""}
	listRoots(f, wtRoot)
	admin := readGitdirTarget(t, wtRoot)
	writeIndex(t, filepath.Join(mainRoot, ".git"), buildIndex(2, nil))
	writeIndex(t, admin, buildIndex(2, []idxEntry{{path: ".mtix/data/mtix.db"}}))
	got := EvaluateGlue(context.Background(), f.deps(), wtRoot)
	assert.False(t, got.Act)
	assert.Equal(t, ReasonTrackedStore, got.Reason, "the worktree's index tracks its store")
}

func TestGluePredicate_ForgedGitFileOverListedBoard_FailsClosedForIdentifiedStores(t *testing.T) {
	root := t.TempDir()
	writeFile(t, dbOf(root), "db")
	writeFile(t, filepath.Join(root, ".git"), "gitdir: /nonexistent\n")
	f := &predicateFixture{root: root, id: "inst-1"}
	listRoots(f, root)
	got := EvaluateGlue(context.Background(), f.deps(), root)
	assert.False(t, got.Act, "a .git entry that is not provably a checkout declines even for an identified store")
	assert.Equal(t, ReasonNoCheckout, got.Reason)
}

func TestGluePredicate_StoreFileIsASymlink_IsNotAStore(t *testing.T) {
	f := newPredicateFixture(t)
	elsewhere := filepath.Join(t.TempDir(), "other.db")
	require.NoError(t, os.WriteFile(elsewhere, []byte("db"), 0o644))
	require.NoError(t, os.Remove(dbOf(f.root)))
	require.NoError(t, os.Symlink(elsewhere, dbOf(f.root)))
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	assert.False(t, got.Act, "a symlink named mtix.db must not lend another database")
	assert.Equal(t, ReasonIdentityMismatch, got.Reason)
	assert.Empty(t, f.reads)
}

func TestGluePredicate_StoreDataDirIsASymlink_IsNotAStore(t *testing.T) {
	f := newPredicateFixture(t)
	elsewhere := filepath.Join(t.TempDir(), "data")
	writeFile(t, filepath.Join(elsewhere, "mtix.db"), "db")
	require.NoError(t, os.RemoveAll(filepath.Join(f.root, ".mtix", "data")))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(f.root, ".mtix", "data")))
	got := EvaluateGlue(context.Background(), f.deps(), f.root)
	assert.False(t, got.Act)
	assert.Equal(t, ReasonIdentityMismatch, got.Reason)
}

// A plain file named .mtix is not a store: the verbs skip it and so does the
// predicate, which keeps looking above it.
func TestGluePredicate_MtixThatIsAFile_IsSkippedLikeTheVerbsDo(t *testing.T) {
	f := newPredicateFixture(t)
	sub := filepath.Join(f.root, "sub")
	writeFile(t, filepath.Join(sub, ".mtix"), "not a directory")
	got := EvaluateGlue(context.Background(), f.deps(), sub)
	require.True(t, got.Act, got.Reason)
	assert.Equal(t, f.root, got.BoardRoot)
}
