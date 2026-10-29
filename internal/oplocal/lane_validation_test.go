//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateLaneLocation_InvalidInputs_Refuse(t *testing.T) {
	for _, kind := range []string{"harness", "board", "relative-root", "relative-path", "outside", "missing-root", "missing-path", "file-root"} {
		t.Run(kind, func(t *testing.T) {
			loc := laneFixture(t)
			switch kind {
			case "harness":
				loc.Harness = "bad harness"
			case "board":
				loc.BoardKey = "bad"
			case "relative-root":
				loc.Root = "relative"
			case "relative-path":
				loc.Path = "relative"
			case "outside":
				loc.Path = t.TempDir()
			case "missing-root":
				loc.Root = filepath.Join(loc.Root, "absent")
			case "missing-path":
				loc.Path = filepath.Join(loc.Path, "absent")
			case "file-root":
				file := filepath.Join(loc.Root, "file")
				require.NoError(t, os.WriteFile(file, nil, 0600))
				loc.Root, loc.Path = file, file
			}
			require.Error(t, ValidateLaneLocation(loc))
		})
	}
}
func TestLaneMetadata_InvalidFiles_Refuse(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "large", "empty", "multiline", "carriage-return", "nul", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "metadata")
			switch kind {
			case "directory":
				require.NoError(t, os.Mkdir(path, 0700))
			case "symlink":
				require.NoError(t, os.Symlink("missing", path))
			default:
				body := "ok"
				switch kind {
				case "large":
					body = strings.Repeat("x", 4097)
				case "empty":
					body = ""
				case "multiline":
					body = "a\nb\n"
				case "carriage-return":
					body = "a\r"
				case "nul":
					body = "a\x00"
				}
				require.NoError(t, os.WriteFile(path, []byte(body), 0600))
				if kind == "unreadable" {
					require.NoError(t, os.Chmod(path, 0000))
					t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0600)) })
				}
			}
			_, err := laneMetadata(path)
			require.Error(t, err)
		})
	}
}
func TestLearnLane_InvalidOrForeignRecords_DoNotOverwrite(t *testing.T) {
	for _, kind := range []string{"invalid-id", "duplicate-id", "empty-ids", "duplicate-location", "foreign-host"} {
		t.Run(kind, func(t *testing.T) {
			s, loc := fixture(t), laneFixture(t)
			require.NoError(t, s.LearnLane(t.Context(), loc, "lane-a"))
			rows, err := readRouting[[]learnedLane](s, "lanes")
			require.NoError(t, err)
			switch kind {
			case "invalid-id":
				rows[0].IDs = []string{"bad\nid"}
			case "duplicate-id":
				rows[0].IDs = []string{"lane-a", "lane-a"}
			case "empty-ids":
				rows[0].IDs = nil
			case "duplicate-location":
				rows = append(rows, rows[0])
			}
			require.NoError(t, s.WriteJSON("lanes", rows))
			if kind == "foreign-host" {
				path := filepath.Join(s.pathForTest(t), "lanes.json")
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				host, err := s.HostID()
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(raw), host, strings.Repeat("0", 32), 1)), 0600))
			}
			require.Error(t, s.LearnLane(t.Context(), loc, "new-lane"))
			ids, err := s.LaneIDs(t.Context(), loc)
			require.Error(t, err)
			require.Empty(t, ids)
		})
	}
}
func TestLearnLane_SpacesInValidID_PreservesExistingIdentityGrammar(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	require.NoError(t, s.LearnLane(t.Context(), loc, "lane with spaces"))
	ids, err := s.LaneIDs(t.Context(), loc)
	require.NoError(t, err)
	require.Equal(t, []string{"lane with spaces"}, ids)
	require.Error(t, s.LearnLane(t.Context(), loc, "\ninvalid"))
}
func TestListBoard_ConflictingIdentityOrInvalidRecord_Refuses(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	require.NoError(t, s.ListBoard(t.Context(), loc.BoardKey, loc.Root))
	require.Error(t, s.ListBoard(t.Context(), "bad", loc.Root))
	require.Error(t, s.ListBoard(t.Context(), loc.BoardKey, "relative"))
	require.Error(t, s.ListBoard(t.Context(), "22222222-2222-4222-8222-222222222222", loc.Root))
	require.NoError(t, s.WriteJSON("listed-boards", []ListedBoard{{Key: "bad", Path: loc.Root}}))
	require.Error(t, s.ListBoard(t.Context(), loc.BoardKey, loc.Root))
	_, err := s.ListedBoards(t.Context())
	require.Error(t, err)
}

func TestLearnLane_CommitFailure_PreservesPriorEverLearnedIDs(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	require.NoError(t, s.LearnLane(t.Context(), loc, "lane-a"))
	s.beforeCommit = func() error { return os.ErrPermission }
	require.ErrorIs(t, s.LearnLane(t.Context(), loc, "lane-b"), os.ErrPermission)
	ids, err := s.LaneIDs(t.Context(), loc)
	require.NoError(t, err)
	require.Equal(t, []string{"lane-a"}, ids)
	s.beforeCommit = nil
	require.NoError(t, s.LearnLane(t.Context(), loc, "lane-b"))
	ids, err = s.LaneIDs(t.Context(), loc)
	require.NoError(t, err)
	require.Equal(t, []string{"lane-a", "lane-b"}, ids)
}

func TestLaneRepository_MalformedOrBrokenMetadata_Refuses(t *testing.T) {
	for _, kind := range []string{"marker", "empty-file", "missing-common", "missing-back-target"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			common := filepath.Join(t.TempDir(), ".git")
			admin := filepath.Join(common, "worktrees", "lane")
			require.NoError(t, os.MkdirAll(admin, 0700))
			dot := filepath.Join(root, ".git")
			body := "gitdir: " + admin + "\n"
			if kind == "marker" {
				body = "not-a-marker\n"
			}
			if kind == "empty-file" {
				body = ""
			}
			require.NoError(t, os.WriteFile(dot, []byte(body), 0600))
			commonText := common
			if kind == "missing-common" {
				commonText = filepath.Join(common, "absent")
			}
			require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(commonText+"\n"), 0600))
			back := dot
			if kind == "missing-back-target" {
				back = filepath.Join(root, "absent")
			}
			require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(back+"\n"), 0600))
			_, _, err := laneRepository(root)
			require.Error(t, err)
		})
	}
}
func TestRoutingRecords_MalformedPayload_RefusesReadAndWrite(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	require.NoError(t, s.LearnLane(t.Context(), loc, "lane-a"))
	require.NoError(t, s.ListBoard(t.Context(), loc.BoardKey, loc.Root))
	require.NoError(t, s.WriteJSON("lanes", 123))
	require.Error(t, s.LearnLane(t.Context(), loc, "lane-b"))
	_, err := s.LaneIDs(t.Context(), loc)
	require.Error(t, err)
	require.NoError(t, s.WriteJSON("listed-boards", 123))
	require.Error(t, s.ListBoard(t.Context(), loc.BoardKey, loc.Root))
	_, err = s.ListedBoards(t.Context())
	require.Error(t, err)
}
func TestListBoard_TwoCheckouts_ReturnsBothInStableOrder(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	other := t.TempDir()
	require.NoError(t, s.ListBoard(t.Context(), loc.BoardKey, loc.Root))
	require.NoError(t, s.ListBoard(t.Context(), loc.BoardKey, other))
	rows, err := s.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Less(t, rows[0].Path, rows[1].Path)
}
