// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCanonicalExclusion_BackslashNameAndInvalidRoots_PreservesNameOrReturnsCause(t *testing.T) {
	root := safeFixtureRoot(t)
	expected, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	result, err := canonicalExclusion(filepath.Join(root, `input\next`), canonicalExistingPath)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(expected, `input\next`), result)
	result, err = canonicalExclusion(root, func(string) (string, error) { return "", os.ErrPermission })
	require.Empty(t, result)
	require.ErrorIs(t, err, os.ErrPermission)
	result, err = canonicalExclusion(root, func(string) (string, error) { return "relative", nil })
	require.Empty(t, result)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	for _, input := range []string{"", root + "\x00", root + string([]byte{0xff})} {
		result, err = canonicalExclusion(input, canonicalExistingPath)
		require.Empty(t, result)
		require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	}
}

func TestState_UnreadableExclusionRoot_SkipsWithDebugLog(t *testing.T) {
	for _, operation := range stateOperations() {
		t.Run(operation, func(t *testing.T) {
			root := safeFixtureRoot(t)
			excluded := filepath.Join(root, "input")
			require.NoError(t, os.Mkdir(excluded, 0700))
			require.NoError(t, os.Chmod(excluded, 0))
			t.Cleanup(func() { require.NoError(t, os.Chmod(excluded, 0700)) })
			var log bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(previous) })
			s := New(Env{GOOS: "linux", Home: root, Values: map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "config"), "CODEX_HOME": filepath.Join(excluded, "next")}})
			requireStateOperationAllowed(t, s, operation)
			require.Contains(t, log.String(), "permission denied")
		})
	}
}

func TestState_UnresolvableExclusionRoot_SkipsWithDebugLog(t *testing.T) {
	for _, name := range []string{"self_symlink", "dangling_symlink", "nul_byte", "invalid_utf8", "regular_file", "symlink_to_file"} {
		for _, operation := range stateOperations() {
			t.Run(name+"/"+operation, func(t *testing.T) {
				root := safeFixtureRoot(t)
				input := filepath.Join(root, "input")
				switch name {
				case "self_symlink":
					require.NoError(t, os.Symlink(input, input))
				case "dangling_symlink":
					require.NoError(t, os.Symlink(filepath.Join(root, "absent"), input))
				case "nul_byte":
					input += "\x00"
				case "invalid_utf8":
					input += string([]byte{0xff})
				case "regular_file":
					require.NoError(t, os.WriteFile(input, nil, 0600))
				case "symlink_to_file":
					file := filepath.Join(root, "file")
					require.NoError(t, os.WriteFile(file, nil, 0600))
					require.NoError(t, os.Symlink(file, input))
				}
				var log bytes.Buffer
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
				t.Cleanup(func() { slog.SetDefault(previous) })
				s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "config"), "CODEX_HOME": input}})
				requireStateOperationAllowed(t, s, operation)
				requireExclusionDebugLog(t, log.String(), input)
			})
		}
	}
}

func requireStateOperationAllowed(t *testing.T, s *State, operation string) {
	t.Helper()
	switch operation {
	case "ensure":
		require.NoError(t, s.Ensure())
	case "host":
		_, err := s.HostID()
		require.NoError(t, err)
	case "write":
		require.NoError(t, s.WriteJSON("hooks", true))
	case "read":
		got := map[string]bool{"old": true}
		_, err := s.ReadJSON("hooks", &got)
		require.NoError(t, err)
		require.Nil(t, got)
	case "inspect":
		status, err := s.Inspect()
		require.NoError(t, err)
		require.Equal(t, "absent", status.Status)
	}
}

func TestInside_PlatformCaseRules_MatchesOnlyContainedPaths(t *testing.T) {
	for _, platform := range []string{"darwin", "windows"} {
		require.True(t, inside(platform, "/volume/CONFIG/next", "/VOLUME/config"))
		require.False(t, inside(platform, "/volume/config-next", "/VOLUME/config"))
	}
	require.False(t, inside("linux", "/volume/CONFIG/next", "/VOLUME/config"))
}

func TestState_ProspectiveExclusionRoot_RefusesBeforeAndAfterCreation(t *testing.T) {
	root := safeFixtureRoot(t)
	excluded := filepath.Join(root, "later")
	s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "config"), "CODEX_HOME": excluded}})
	require.NoError(t, s.Ensure())
	s.env.Values["XDG_CONFIG_HOME"] = filepath.Join(excluded, "config")
	require.ErrorIs(t, s.Ensure(), model.ErrOperatorStateUnreadable)
	requireMissingDirectory(t, excluded)
	require.NoError(t, os.Mkdir(excluded, 0700))
	require.ErrorIs(t, s.Ensure(), model.ErrOperatorStateUnreadable)
	requireMissingDirectory(t, filepath.Join(excluded, "config"))
}

func TestState_DarwinTemporaryRootCaseAlias_Refuses(t *testing.T) {
	if runtime.GOOS != "darwin" {
		return
	}
	root := safeFixtureRoot(t)
	excluded := filepath.Join(root, "Input")
	require.NoError(t, os.Mkdir(excluded, 0700))
	for _, operation := range stateOperations() {
		s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": filepath.Join(excluded, "config"), "TMPDIR": strings.ToUpper(excluded)}})
		requireLocationRefused(t, s, operation, filepath.Join(excluded, "config"), nil)
	}
}

func TestState_AliasParentExclusionSibling_AllowsFirstUse(t *testing.T) {
	root := safeFixtureRoot(t)
	resolved := filepath.Join(root, "resolved")
	require.NoError(t, os.MkdirAll(filepath.Join(resolved, "deeper"), 0700))
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(filepath.Join(resolved, "deeper"), alias))
	s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "missing", "config"), "CODEX_HOME": alias + "/../missing"}})
	require.NoError(t, s.Ensure())
	require.NoError(t, s.WriteJSON("hooks", true))
}

func requireExclusionDebugLog(t *testing.T, text, input string) {
	t.Helper()
	found := false
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		if row["root"] == strings.ToValidUTF8(input, "\ufffd") {
			require.Equal(t, "DEBUG", row["level"])
			require.NotEmpty(t, row["error"])
			found = true
		}
	}
	require.True(t, found)
}

func TestState_ExcludedBackslashName_RefusesBeforeCreation(t *testing.T) {
	root := safeFixtureRoot(t)
	excluded := filepath.Join(root, `input\next`)
	for _, operation := range stateOperations() {
		s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": filepath.Join(excluded, "config"), "CODEX_HOME": excluded}})
		requireLocationRefused(t, s, operation, excluded, nil)
	}
	dir, err := openDirectory(filepath.Join(excluded, "config", "mtix"), true, []string{excluded})
	if dir != nil {
		require.NoError(t, dir.Close())
	}
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	requireMissingDirectory(t, excluded)
}

func TestInside_BackslashSeparatorRules_DistinguishesUnixNameFromWindowsPath(t *testing.T) {
	require.False(t, inside("darwin", `/volume/input\next/config`, "/volume/input/next"))
	require.True(t, inside("windows", `C:\volume\input\next\config`, `c:\VOLUME\INPUT\NEXT`))
	root := safeFixtureRoot(t)
	s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, `input\next`, "config"), "CODEX_HOME": filepath.Join(root, "input", "next")}})
	require.NoError(t, s.Ensure())
}
