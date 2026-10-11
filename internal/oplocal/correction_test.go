// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package oplocal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestEnsure_InsideGitWorkTree_Refuses(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	require.NoError(t, os.WriteFile(filepath.Join(s.env.root, "home/test/.git"), []byte("gitdir: other"), 0600))
	require.ErrorIs(t, s.Ensure(), model.ErrOperatorStateUnreadable)
}
func TestEnsure_SymlinkComponent_Refuses(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	dir := s.pathForTest(t)
	require.NoError(t, os.Rename(dir, dir+"-old"))
	require.NoError(t, os.Symlink(dir+"-old", dir))
	require.ErrorIs(t, s.Ensure(), model.ErrOperatorStateUnreadable)
}
func TestEnsure_UnderCodexHome_Refuses(t *testing.T) {
	s := fixture(t)
	s.env.Values["CODEX_HOME"] = "/home/test/.config"
	require.ErrorIs(t, s.Ensure(), model.ErrOperatorStateUnreadable)
}
func TestReadJSON_CorruptFile_ReturnsErrOperatorStateUnreadable(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.WriteJSON("hooks", nil))
	require.NoError(t, os.WriteFile(filepath.Join(s.pathForTest(t), "hooks.json"), []byte("{"), 0600))
	var got any
	_, err := s.ReadJSON("hooks", &got)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}
func TestReadJSON_ModeTooWide_FailsClosed(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.WriteJSON("hooks", nil))
	require.NoError(t, os.Chmod(filepath.Join(s.pathForTest(t), "hooks.json"), 0644))
	var got any
	_, err := s.ReadJSON("hooks", &got)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}
func TestReadJSON_MissingFile_ReturnsEmpty(t *testing.T) {
	s := fixture(t)
	_, err := s.HostID()
	require.NoError(t, err)
	got := map[string]bool{"old": true}
	reports, err := s.ReadJSON("missing", &got)
	require.NoError(t, err)
	require.Empty(t, reports)
	require.Nil(t, got)
}
func TestEnsure_WritableAncestor_Refuses(t *testing.T) {
	for _, mode := range []os.FileMode{0775, 0777, os.ModeSticky | 0777} {
		t.Run(mode.String(), func(t *testing.T) {
			s := fixture(t)
			require.NoError(t, s.Ensure())
			parent := filepath.Dir(s.pathForTest(t))
			require.NoError(t, os.Chmod(parent, mode))
			require.ErrorIs(t, s.Ensure(), model.ErrOperatorStateUnreadable)
		})
	}
}
func TestInspect_AbsentState_ReportsPlacementLimit(t *testing.T) {
	status, err := fixture(t).Inspect()
	require.NoError(t, err)
	body, err := json.Marshal(status)
	require.NoError(t, err)
	require.Contains(t, string(body), "sandbox write access")
}
func TestInspect_TemporaryRecord_ReportsWithoutRemoval(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	file := filepath.Join(s.pathForTest(t), ".state-incomplete")
	require.NoError(t, os.WriteFile(file, nil, 0600))
	status, err := s.Inspect()
	require.NoError(t, err)
	require.NotEmpty(t, status.Reports)
	_, err = os.Stat(file)
	require.NoError(t, err)
}

func TestEnsure_TemporaryPathAlias_Refuses(t *testing.T) {
	base := "/var/tmp"
	if runtime.GOOS == "darwin" {
		base = "/private/tmp"
	}
	root, err := os.MkdirTemp(base, "mtix-state-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	actual, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	s := New(Env{GOOS: runtime.GOOS, Home: actual, Values: map[string]string{"XDG_CONFIG_HOME": actual}})
	require.ErrorIs(t, s.Ensure(), model.ErrOperatorStateUnreadable)
	_, err = os.Stat(filepath.Join(actual, "mtix"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
func TestAncestor_UnsafeMetadata_Refuses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		uid, mode uint32
	}{
		{"foreign_owner", uint32(os.Geteuid() + 1), 0040755}, {"group_writable", uint32(os.Geteuid()), 0040775},
		{"other_writable", 0, 0040777}, {"regular_file", 0, 0100755},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, validateAncestor(tc.uid, tc.mode), model.ErrOperatorStateUnreadable)
		})
	}
	require.NoError(t, validateAncestor(0, 0040755))
	require.NoError(t, validateAncestor(uint32(os.Geteuid()), 0040700))
	s := fixture(t)
	require.NoError(t, s.Ensure())
	dir, err := s.directory(false)
	require.NoError(t, err)
	require.NoError(t, dir.Close())
	require.ErrorIs(t, verifyAncestor(dir), model.ErrOperatorStateUnreadable)
}
func TestWriteData_CleanupFailure_ReturnsSentinelAfterCommit(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	dir, err := s.directory(false)
	require.NoError(t, err)
	defer func() { require.NoError(t, dir.Close()) }()
	syncDirectory := func(_ *os.File) error {
		names, readErr := os.ReadDir(s.pathForTest(t))
		if readErr != nil {
			return readErr
		}
		for _, entry := range names {
			if strings.HasPrefix(entry.Name(), ".state-") {
				path := filepath.Join(s.pathForTest(t), entry.Name())
				if removeErr := os.Remove(path); removeErr != nil {
					return removeErr
				}
				return os.Mkdir(path, 0700)
			}
		}
		return nil
	}
	err = writeData(dir, "host-id", []byte("0123456789abcdef0123456789abcdef\n"), true, nil, syncDirectory)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	body, err := os.ReadFile(filepath.Join(s.pathForTest(t), "host-id"))
	require.NoError(t, err)
	require.Equal(t, "0123456789abcdef0123456789abcdef\n", string(body))
}

func TestEnsure_ConfiguredRootAlias_RefusesBeforeCreation(t *testing.T) {
	root := safeFixtureRoot(t)
	harness := filepath.Join(root, "harness")
	require.NoError(t, os.Mkdir(harness, 0700))
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(harness, alias))
	config := filepath.Join(harness, "config")
	s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": config, "CODEX_HOME": alias}})
	require.ErrorIs(t, s.Ensure(), model.ErrOperatorStateUnreadable)
	_, err := os.Stat(config)
	require.ErrorIs(t, err, os.ErrNotExist)
}
func TestEnsure_CyclicExclusionRoot_SkipsAndCreatesState(t *testing.T) {
	root := safeFixtureRoot(t)
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(alias, alias))
	config := filepath.Join(root, "config")
	s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": config, "CODEX_HOME": alias}})
	require.NoError(t, s.Ensure())
	_, err := os.Stat(config)
	require.NoError(t, err)
}
func TestEnsure_PlatformPathMismatch_RefusesBeforeCreation(t *testing.T) {
	s := New(Env{GOOS: "windows", Values: map[string]string{"APPDATA": `C:\Users\test\AppData`}})
	require.ErrorIs(t, s.Ensure(), model.ErrOperatorStateUnreadable)
}
