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
				s := New(Env{GOOS: "windows", Home: root, Values: map[string]string{"APPDATA": config, "CODEX_HOME": filepath.Join(excluded, "missing", "next")}})
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
