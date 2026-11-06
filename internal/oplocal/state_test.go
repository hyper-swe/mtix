// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package oplocal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T) *State {
	t.Helper()
	root, err := filepath.EvalSymlinks(safeFixtureRoot(t))
	require.NoError(t, err)
	return New(Env{GOOS: "linux", Home: "/home/test", root: root, Values: map[string]string{"TMPDIR": "/temp"}})
}
func TestDir_XDGUnset_UsesDotConfigOnDarwinAndLinux(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			dir, err := Dir(Env{GOOS: platform, Home: "/home/test"})
			require.NoError(t, err)
			require.Equal(t, "/home/test/.config/mtix", dir)
		})
	}
	dir, err := Dir(Env{GOOS: "windows", Values: map[string]string{"APPDATA": `C:\Users\test\AppData\Roaming`}})
	require.NoError(t, err)
	require.Equal(t, `C:\Users\test\AppData\Roaming\mtix`, dir)
}
func TestState_ValidRecords_RoundTrips(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	id, err := s.HostID()
	require.NoError(t, err)
	require.Regexp(t, "^[0-9a-f]{32}$", id)
	again, err := s.HostID()
	require.NoError(t, err)
	require.Equal(t, id, again)
	require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 7}))
	var got map[string]int
	report, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Empty(t, report)
	require.Equal(t, 7, got["value"])
}
func TestReadJSON_MissingDirectory_ReturnsEmpty(t *testing.T) {
	s := fixture(t)
	var got map[string]int
	report, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Empty(t, report)
	require.Nil(t, got)
}
func TestEnsure_DisallowedLocationOrMetadata_Refuses(t *testing.T) {
	cases := []struct {
		name   string
		env    func(*State)
		change func(*testing.T, *State)
	}{
		{"temporary_directory", func(s *State) { s.env.Values["XDG_CONFIG_HOME"] = "/tmp" }, nil},
		{"configured_harness_home", func(s *State) { s.env.Values["CODEX_HOME"] = "/home/test/.config" }, nil},
		{"git_worktree_file", nil, func(t *testing.T, s *State) {
			require.NoError(t, os.WriteFile(filepath.Join(s.env.root, "home/test/.git"), []byte("gitdir: test"), 0600))
		}},
		{"wide_directory_mode", nil, func(t *testing.T, s *State) { require.NoError(t, os.Chmod(s.pathForTest(t), 0755)) }},
		{"symlink_directory", nil, func(t *testing.T, s *State) {
			require.NoError(t, os.Rename(s.pathForTest(t), s.pathForTest(t)+"-old"))
			require.NoError(t, os.Symlink(s.pathForTest(t)+"-old", s.pathForTest(t)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			require.NoError(t, s.Ensure())
			if tc.env != nil {
				tc.env(s)
			}
			if tc.change != nil {
				tc.change(t, s)
			}
			require.ErrorIs(t, s.Ensure(), model.ErrOperatorStateUnreadable)
		})
	}
}
func TestReadJSON_InvalidRecord_ReturnsSentinel(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		mode       os.FileMode
	}{
		{"corrupt_json", "{", 0600}, {"wide_record_mode", `{"host_id":"%s","data":{}}`, 0644}, {"missing_host_id", `{"data":{}}`, 0600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			require.NoError(t, s.Ensure())
			id, err := s.HostID()
			require.NoError(t, err)
			body := tc.body
			if tc.mode == 0644 {
				body = fmt.Sprintf(body, id)
			}
			require.NoError(t, os.WriteFile(filepath.Join(s.pathForTest(t), "hooks.json"), []byte(body), tc.mode))
			var got map[string]int
			_, err = s.ReadJSON("hooks", &got)
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		})
	}
}
func TestHostRecords_OtherHostID_IgnoredAndReported(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	_, err := s.HostID()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(s.pathForTest(t), "hooks.json"), []byte(`{"host_id":"00000000000000000000000000000000","data":{"value":9}}`), 0600))
	var got map[string]int
	report, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.NotEmpty(t, report)
	require.Nil(t, got)
}
func TestWriteJSON_CrashBeforeRename_LeavesOldFile(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 1}))
	s.beforeCommit = func() error { return errors.New("operation failed") }
	require.ErrorIs(t, s.WriteJSON("hooks", map[string]int{"value": 2}), model.ErrOperatorStateUnreadable)
	var got map[string]int
	_, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, 1, got["value"])
}
func TestHostID_ConcurrentFirstUse_ReturnsSameID(t *testing.T) {
	s := fixture(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	ids := make([]string, 8)
	errs := make([]error, 8)
	for i := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; ids[i], errs[i] = s.HostID() }()
	}
	close(start)
	wg.Wait()
	for i := range ids {
		require.NoError(t, errs[i])
		require.Equal(t, ids[0], ids[i])
	}
}
