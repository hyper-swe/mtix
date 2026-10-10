// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"bytes"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestWalkDirectory_ValidateInput(t *testing.T) {
	root := safeFixtureRoot(t)
	target := filepath.Join(root, "config")
	initial, err := os.Open("/")
	require.NoError(t, err)
	check := func(dir *os.File) error {
		if dir == initial {
			return invalid("validate input")
		}
		return verifyAncestor(dir)
	}
	result, err := walkDirectoryChecked(initial, target, true, check)
	if result != nil {
		require.NoError(t, result.Close())
	}
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	requireMissingDirectory(t, target)
	_, err = initial.Stat()
	require.ErrorIs(t, err, os.ErrClosed)
}

func TestCanonicalExclusion_ValidateInput(t *testing.T) {
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

func TestState_ValidateInputPermissions(t *testing.T) {
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
