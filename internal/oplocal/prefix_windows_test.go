// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package oplocal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestState_ValidateConfiguration(t *testing.T) {
	for _, spelling := range []string{"case01", "case02", "case03", "case04", "case05"} {
		for _, operation := range stateOperations() {
			t.Run(spelling+"/"+operation, func(t *testing.T) {
				root := safeFixtureRoot(t)
				prefix := filepath.Join(root, "existing-prefix")
				require.NoError(t, os.Mkdir(prefix, 0700))
				excluded := windowsPrefixSpelling(t, root, prefix, spelling)
				absent := filepath.Join(prefix, "missing")
				config := filepath.Join(absent, "next", "config")
				input := excluded + `\missing\next`
				s := New(Env{GOOS: "windows", Home: root, Values: map[string]string{"APPDATA": config, "CODEX_HOME": input}})
				require.Equal(t, excluded+`\missing\next`, s.env.Values["CODEX_HOME"])
				if spelling == "case05" {
					require.Equal(t, prefix+`\..\other\missing\next`, s.env.Values["CODEX_HOME"])
					require.Contains(t, s.env.Values["CODEX_HOME"], `\..\`)
				}
				requireLocationRefused(t, s, operation, absent, nil)
			})
		}
	}
}

func windowsPrefixSpelling(t *testing.T, root, prefix, spelling string) string {
	t.Helper()
	switch spelling {
	case "case05":
		return prefix + `\..\other`
	case "case01":
		return `\\?\` + prefix
	case "case02":
		setFixtureWorkingDirectory(t, root)
		return filepath.Base(prefix)
	case "case04":
		alias := filepath.Join(root, "alias")
		err := os.Symlink(prefix, alias)
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) || errors.Is(err, os.ErrPermission) {
			t.Skip("directory alias creation requires unavailable privileges")
		}
		require.NoError(t, err)
		return alias
	case "case03":
		text, err := windows.UTF16PtrFromString(prefix)
		require.NoError(t, err)
		buffer := make([]uint16, 32768)
		count, err := windows.GetShortPathName(text, &buffer[0], uint32(len(buffer)))
		require.NoError(t, err)
		require.Less(t, count, uint32(len(buffer)))
		short := windows.UTF16ToString(buffer[:count])
		if strings.EqualFold(short, prefix) {
			t.Skip("short path names are unavailable on this volume")
		}
		return short
	}
	t.Fatal("unknown path spelling")
	return ""
}

func TestOpenDirectory_ValidateConfiguration(t *testing.T) {
	root := safeFixtureRoot(t)
	absent := filepath.Join(root, "missing")
	_, err := openDirectory(filepath.Join(absent, "next", "mtix"), true, []string{absent})
	require.Error(t, err)
	requireMissingDirectory(t, absent)
}

func TestState_ValidateMetadata(t *testing.T) {
	for _, operation := range stateOperations() {
		t.Run(operation, func(t *testing.T) {
			root := safeFixtureRoot(t)
			alias := filepath.Join(root, "alias")
			err := os.Symlink(filepath.Join(root, "absent"), alias)
			if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) || errors.Is(err, os.ErrPermission) {
				t.Skip("directory alias creation requires unavailable privileges")
			}
			require.NoError(t, err)
			config := filepath.Join(root, "config")
			s := New(Env{GOOS: "windows", Home: root, Values: map[string]string{"APPDATA": config, "CODEX_HOME": alias}})
			requireLocationRefused(t, s, operation, config, os.ErrNotExist)
		})
	}
}

func TestState_ValidateConfigurationControls(t *testing.T) {
	root := safeFixtureRoot(t)
	prefix := filepath.Join(root, "existing-prefix")
	require.NoError(t, os.Mkdir(prefix, 0700))
	setFixtureWorkingDirectory(t, root)
	absent := filepath.Join(prefix, "missing")
	s := New(Env{GOOS: "windows", Home: root, Values: map[string]string{
		"APPDATA": filepath.Join(absent, "next", "config"), "CODEX_HOME": `.\other\missing\next`,
	}})
	require.Equal(t, `.\other\missing\next`, s.env.Values["CODEX_HOME"])
	got := map[string]int{"old": 1}
	reports, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Empty(t, reports)
	require.Nil(t, got)
	status, err := s.Inspect()
	require.NoError(t, err)
	require.Equal(t, "absent", status.Status)
	requireMissingDirectory(t, absent)
	require.NoError(t, s.Ensure())
	id, err := s.HostID()
	require.NoError(t, err)
	require.Regexp(t, "^[0-9a-f]{32}$", id)
	again, err := s.HostID()
	require.NoError(t, err)
	require.Equal(t, id, again)
	require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 7}))
	reports, err = s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Empty(t, reports)
	require.Equal(t, map[string]int{"value": 7}, got)
	status, err = s.Inspect()
	require.NoError(t, err)
	require.Equal(t, "ready", status.Status)
}
