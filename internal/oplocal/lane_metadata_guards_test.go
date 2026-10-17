//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func laneWorktreeFixture(t *testing.T, common, name, key string) LaneLocation {
	t.Helper()
	root := t.TempDir()
	admin := filepath.Join(common, "worktrees", name)
	require.NoError(t, os.MkdirAll(admin, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+admin+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(root, ".git")+"\n"), 0600))
	return LaneLocation{Harness: "codex", BoardKey: key, Root: root, Path: root}
}

func TestLearnLane_WorktreeLearning_DoesNotWriteSharedMainFallback(t *testing.T) {
	s, main := fixture(t), laneFixture(t)
	common := filepath.Join(main.Root, ".git")
	require.NoError(t, os.MkdirAll(common, 0700))
	one := laneWorktreeFixture(t, common, "one", "22222222-2222-4222-8222-222222222222")
	two := laneWorktreeFixture(t, common, "two", "33333333-3333-4333-8333-333333333333")
	require.NoError(t, s.LearnLane(t.Context(), one, "worktree-one"))
	ids, err := s.LaneIDs(t.Context(), one)
	require.NoError(t, err)
	require.Equal(t, []string{"worktree-one"}, ids)
	ids, err = s.LaneIDs(t.Context(), two)
	require.NoError(t, err)
	require.Empty(t, ids)
	ids, err = s.LaneIDs(t.Context(), main)
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestListBoard_ChangedIdentity_ReturnsConflictWithoutUnreadableLabel(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	require.NoError(t, s.ListBoard(t.Context(), loc.BoardKey, loc.Root))
	err := s.ListBoard(t.Context(), "22222222-2222-4222-8222-222222222222", loc.Root)
	require.ErrorIs(t, err, model.ErrConflict)
	require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestLanePaths_MissingDirectory_WrapsRootAndWorkingPathCauses(t *testing.T) {
	for _, kind := range []string{"root", "working"} {
		t.Run(kind, func(t *testing.T) {
			loc := laneFixture(t)
			want := "resolve lane working path"
			if kind == "root" {
				loc.Root = filepath.Join(loc.Root, "missing")
				loc.Path = loc.Root
				want = "resolve lane worktree root"
			} else {
				loc.Path = filepath.Join(loc.Root, "missing")
			}
			_, _, _, _, err := lanePaths(loc)
			require.ErrorIs(t, err, os.ErrNotExist)
			require.ErrorContains(t, err, want)
		})
	}
}

func TestLaneMetadata_FIFO_RefusesBeforeOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".git")
	require.NoError(t, unix.Mkfifo(path, 0600))
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() { close(started); _, err := laneMetadata(path); done <- err }()
	<-started
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-timer.C:
		select {
		case err := <-done:
			require.Error(t, err)
			return
		default:
		}
		t.Error("FIFO metadata must be refused before opening")
		releaseMetadataFIFOReader(t, path, done)
	}
}

func releaseMetadataFIFOReader(t *testing.T, path string, done <-chan error) {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0600)
	require.NoError(t, err)
	defer func() { require.NoError(t, unix.Close(fd)) }()
	_, writeErr := unix.Write(fd, []byte("invalid\n"))
	require.NoError(t, writeErr)
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("owned FIFO reader did not join")
	}
}
