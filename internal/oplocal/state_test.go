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
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return New(Env{GOOS: "linux", Home: "/home/test", Root: root, Values: map[string]string{"TMPDIR": "/temp"}})
}
func TestDir_Defaults(t *testing.T) {
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
func TestState_ValidInput(t *testing.T) {
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
func TestReadJSON_EmptyState(t *testing.T) {
	s := fixture(t)
	var got map[string]int
	report, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Empty(t, report)
	require.Nil(t, got)
}
func TestState_ValidateInput(t *testing.T) {
	cases := []struct {
		name   string
		env    func(*State)
		change func(*testing.T, *State)
	}{
		{"input_a", func(s *State) { s.env.Values["XDG_CONFIG_HOME"] = "/tmp" }, nil},
		{"input_b", func(s *State) { s.env.Values["CODEX_HOME"] = "/home/test/.config" }, nil},
		{"input_c", nil, func(t *testing.T, s *State) {
			require.NoError(t, os.WriteFile(filepath.Join(s.env.Root, "home/test/.git"), []byte("gitdir: test"), 0600))
		}},
		{"input_d", nil, func(t *testing.T, s *State) { require.NoError(t, os.Chmod(s.path, 0755)) }},
		{"input_e", nil, func(t *testing.T, s *State) {
			require.NoError(t, os.Rename(s.path, s.path+"-old"))
			require.NoError(t, os.Symlink(s.path+"-old", s.path))
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
func TestReadJSON_ValidateInput(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		mode       os.FileMode
	}{
		{"input_a", "{", 0600}, {"input_b", `{"host_id":"%s","data":{}}`, 0644}, {"input_c", `{"data":{}}`, 0600},
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
			require.NoError(t, os.WriteFile(filepath.Join(s.path, "hooks.json"), []byte(body), tc.mode))
			var got map[string]int
			_, err = s.ReadJSON("hooks", &got)
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		})
	}
}
func TestHostRecords_ValidateInput(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	_, err := s.HostID()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(s.path, "hooks.json"), []byte(`{"host_id":"00000000000000000000000000000000","data":{"value":9}}`), 0600))
	var got map[string]int
	report, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.NotEmpty(t, report)
	require.Nil(t, got)
}
func TestWriteJSON_PreservesStateOnFailure(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 1}))
	s.beforeCommit = func() error { return errors.New("operation failed") }
	require.ErrorIs(t, s.WriteJSON("hooks", map[string]int{"value": 2}), model.ErrOperatorStateUnreadable)
	var got map[string]int
	_, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, 1, got["value"])
}
func TestHostID_ConcurrentInitialization(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	var wg sync.WaitGroup
	ids := make([]string, 8)
	errs := make([]error, 8)
	for i := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); ids[i], errs[i] = s.HostID() }()
	}
	wg.Wait()
	for i := range ids {
		require.NoError(t, errs[i])
		require.Equal(t, ids[0], ids[i])
	}
}
