// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// OperatorState supplies an isolated operator configuration for hook tests.
func OperatorState(t *testing.T) *oplocal.State {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	root, err := os.MkdirTemp(home, ".mtix-operator-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	key := "HOME"
	if runtime.GOOS == "windows" {
		key = "USERPROFILE"
	}
	t.Setenv(key, root)
	config := filepath.Join(root, "operator")
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("APPDATA", config)
	return oplocal.New(oplocal.Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": config, "APPDATA": config}})
}
