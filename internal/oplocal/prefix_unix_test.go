// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestState_ValidateConfiguration(t *testing.T) {
	for _, spelling := range []string{"case01", "case02", "case03", "case04", "case05", "case06"} {
		for _, operation := range stateOperations() {
			t.Run(spelling+"/"+operation, func(t *testing.T) {
				s, absent := unixPrefixState(t, spelling)
				requireLocationRefused(t, s, operation, absent, nil)
			})
		}
	}
}

func unixPrefixState(t *testing.T, spelling string) (*State, string) {
	t.Helper()
	root := safeFixtureRoot(t)
	resolved := filepath.Join(root, "resolved")
	require.NoError(t, os.Mkdir(resolved, 0700))
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(resolved, alias))
	excluded, absent := filepath.Join(alias, "missing"), filepath.Join(resolved, "missing")
	config := filepath.Join(absent, "config")
	switch spelling {
	case "case02":
		setFixtureWorkingDirectory(t, root)
		excluded, absent = "resolved", filepath.Join(resolved, "config")
		config = absent
	case "case03":
		setFixtureWorkingDirectory(t, root)
		excluded = filepath.Join("alias", "missing")
	case "case04":
		excluded = filepath.Join(alias, "missing", "next")
		config = filepath.Join(absent, "next", "config")
	case "case06":
		excluded = alias + string(filepath.Separator) + "." + string(filepath.Separator) + "missing"
	case "case05":
		deeper := filepath.Join(resolved, "deeper")
		require.NoError(t, os.Mkdir(deeper, 0700))
		require.NoError(t, os.Remove(alias))
		require.NoError(t, os.Symlink(deeper, alias))
		excluded = alias + string(filepath.Separator) + ".." + string(filepath.Separator) + "missing"
	}
	return New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": config, "CODEX_HOME": excluded}}), absent
}

func TestState_ValidateMetadata(t *testing.T) {
	for _, kind := range []string{"case01", "case02", "case03", "case04"} {
		for _, operation := range stateOperations() {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				root := safeFixtureRoot(t)
				excluded := filepath.Join(root, "excluded")
				cause := error(os.ErrNotExist)
				switch kind {
				case "case01":
					require.NoError(t, os.Symlink(filepath.Join(root, "absent"), excluded))
				case "case02":
					require.NoError(t, os.Symlink(excluded, excluded))
					cause = unix.ELOOP
				case "case04":
					excluded = filepath.Join(root, strings.Repeat("x", 300))
					cause = unix.ENAMETOOLONG
				case "case03":
					require.NoError(t, os.WriteFile(excluded, nil, 0600))
					excluded = filepath.Join(excluded, "child")
					cause = unix.ENOTDIR
				}
				config := filepath.Join(root, "config")
				s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": config, "CODEX_HOME": excluded}})
				_ = cause
				requireStateOperationAllowed(t, s, operation)
			})
		}
	}
}

func TestState_ValidateConfigurationControls(t *testing.T) {
	root := safeFixtureRoot(t)
	resolved := filepath.Join(root, "resolved")
	require.NoError(t, os.Mkdir(resolved, 0700))
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(resolved, alias))
	setFixtureWorkingDirectory(t, root)
	config := filepath.Join(resolved, "missing-sibling", "config")
	s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": config, "CODEX_HOME": "alias" + string(filepath.Separator) + "." + string(filepath.Separator) + "missing"}})
	got := map[string]bool{"old": true}
	_, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Nil(t, got)
	status, err := s.Inspect()
	require.NoError(t, err)
	require.Equal(t, "absent", status.Status)
	requireMissingDirectory(t, filepath.Join(resolved, "missing-sibling"))
	require.NoError(t, s.WriteJSON("hooks", true))
	require.NoError(t, s.Ensure())
	_, err = s.HostID()
	require.NoError(t, err)
}

func TestState_ValidateMetadataControls(t *testing.T) {
	for _, operation := range stateOperations() {
		t.Run(operation, func(t *testing.T) {
			root := safeFixtureRoot(t)
			excluded := filepath.Join(root, "file")
			require.NoError(t, os.WriteFile(excluded, nil, 0600))
			config := filepath.Join(root, "config")
			s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": config, "CODEX_HOME": excluded}})
			requireStateOperationAllowed(t, s, operation)
		})
	}
}

func TestState_ValidateProcessInput(t *testing.T) {
	root := safeFixtureRoot(t)
	cwd := filepath.Join(root, "cwd")
	require.NoError(t, os.Mkdir(cwd, 0700))
	setFixtureWorkingDirectory(t, cwd)
	require.NoError(t, os.Remove(cwd))
	if _, err := os.Getwd(); err == nil {
		t.Skip("working directory lookup remains available after removal on this platform")
	}
	for _, operation := range stateOperations() {
		t.Run(operation, func(t *testing.T) {
			config := filepath.Join(root, "config")
			s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": config, "CODEX_HOME": "case02"}})
			requireStateOperationAllowed(t, s, operation)
		})
	}
}
