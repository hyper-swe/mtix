//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func laneFixture(t *testing.T) LaneLocation {
	t.Helper()
	root := t.TempDir()
	return LaneLocation{Harness: "codex", BoardKey: "11111111-1111-4111-8111-111111111111", Root: root, Path: root}
}

func TestLearnLane_TwoIDsAndRestart_PreservesEverLearnedSet(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	for _, id := range []string{"lane-a", "lane-b", "lane-a"} {
		require.NoError(t, s.LearnLane(t.Context(), loc, id))
	}
	ids, err := New(s.env).LaneIDs(t.Context(), loc)
	require.NoError(t, err)
	require.Equal(t, []string{"lane-a", "lane-b"}, ids)
	grants, reports, err := s.BoardGrants()
	require.NoError(t, err)
	require.Empty(t, reports)
	require.Empty(t, grants)
}

func TestLearnLane_ConcurrentWriters_KeepEveryID(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{"lane-a", "lane-b"} {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = New(s.env).LearnLane(t.Context(), loc, id) }()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	ids, err := s.LaneIDs(t.Context(), loc)
	require.NoError(t, err)
	require.Len(t, ids, 2)
}

func TestLaneIDs_NewWorktree_FallsBackOnlyWithMatchingBacklink(t *testing.T) {
	for _, kind := range []string{"valid", "wrong-backlink", "missing-backlink", "direct-common", "outside-admin"} {
		t.Run(kind, func(t *testing.T) {
			s := fixture(t)
			main := t.TempDir()
			common := filepath.Join(main, ".git")
			admin := filepath.Join(common, "worktrees", "new")
			require.NoError(t, os.MkdirAll(admin, 0700))
			wt := t.TempDir()
			dotgit := filepath.Join(wt, ".git")
			target := admin
			if kind == "direct-common" {
				target = common
			}
			if kind == "outside-admin" {
				admin = t.TempDir()
				target = admin
			}
			require.NoError(t, os.WriteFile(dotgit, []byte("gitdir: "+target+"\n"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0600))
			if kind != "missing-backlink" {
				back := dotgit
				if kind == "wrong-backlink" {
					back = filepath.Join(main, ".git")
				}
				require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(back+"\n"), 0600))
			}
			loc := LaneLocation{Harness: "codex", Root: main, Path: main}
			require.NoError(t, s.LearnLane(t.Context(), loc, "lane-main"))
			loc.Root, loc.Path = wt, wt
			ids, err := s.LaneIDs(t.Context(), loc)
			if kind == "valid" {
				require.NoError(t, err)
				require.Equal(t, []string{"lane-main"}, ids)
			} else {
				require.Error(t, err)
				require.Empty(t, ids)
			}
		})
	}
}

func TestListBoard_RepeatedCall_AddsRoutingWithoutGrants(t *testing.T) {
	s := fixture(t)
	loc := laneFixture(t)
	for range 2 {
		require.NoError(t, s.ListBoard(t.Context(), loc.BoardKey, loc.Root))
	}
	boards, err := s.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Len(t, boards, 1)
	grants, _, err := s.BoardGrants()
	require.NoError(t, err)
	require.Empty(t, grants)
}

func TestLearnLane_ForgedMembership_RefusesBeforeCreatingOperatorState(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(loc.Root, ".git"), []byte("gitdir: missing\n"), 0600))
	require.Error(t, s.LearnLane(t.Context(), loc, "lane-a"))
	dir, err := s.Path()
	require.NoError(t, err)
	require.NoDirExists(t, dir)
}
func TestLaneIDs_CorruptRouting_RefusesSilentReuse(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	require.NoError(t, s.LearnLane(t.Context(), loc, "lane-a"))
	require.NoError(t, s.WriteJSON("lanes", []learnedLane{{Key: laneKey{Harness: "invalid harness", Repository: "repo", Relative: "."}, IDs: []string{"lane-a"}}}))
	ids, err := s.LaneIDs(t.Context(), loc)
	require.Error(t, err)
	require.Empty(t, ids)
}

func TestLaneIDs_NoGitBoardIdentityChanged_DoesNotReuseOtherBoard(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	require.NoError(t, s.LearnLane(t.Context(), loc, "other-board-lane"))
	loc.BoardKey = "22222222-2222-4222-8222-222222222222"
	ids, err := s.LaneIDs(t.Context(), loc)
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestLaneIDs_NewWorktreeDistinctBoardID_FallsBackToMainLane(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	common := filepath.Join(loc.Root, ".git")
	admin := filepath.Join(common, "worktrees", "new")
	require.NoError(t, os.MkdirAll(admin, 0700))
	require.NoError(t, s.LearnLane(t.Context(), loc, "lane-main"))
	wt := t.TempDir()
	dot := filepath.Join(wt, ".git")
	require.NoError(t, os.WriteFile(dot, []byte("gitdir: "+admin+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(dot+"\n"), 0600))
	loc.Root, loc.Path, loc.BoardKey = wt, wt, "22222222-2222-4222-8222-222222222222"
	ids, err := s.LaneIDs(t.Context(), loc)
	require.NoError(t, err)
	require.Equal(t, []string{"lane-main"}, ids)
}
