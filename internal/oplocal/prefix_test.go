// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func requireLocationRefused(t *testing.T, s *State, operation, absent string, cause error) {
	t.Helper()
	var err error
	switch operation {
	case "ensure":
		err = s.Ensure()
	case "host":
		_, err = s.HostID()
	case "write":
		err = s.WriteJSON("hooks", map[string]bool{"value": true})
	case "read":
		value := map[string]bool{"old": true}
		_, err = s.ReadJSON("hooks", &value)
		require.Nil(t, value)
	case "inspect":
		var status Status
		status, err = s.Inspect()
		require.NotEqual(t, "absent", status.Status)
	}
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	if cause != nil {
		require.ErrorIs(t, err, cause)
	}
	_, statErr := os.Lstat(absent)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func setFixtureWorkingDirectory(t *testing.T, root string) {
	t.Helper()
	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
}

func stateOperations() []string { return []string{"ensure", "host", "write", "read", "inspect"} }

func requireMissingDirectory(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(filepath.Clean(path))
	require.ErrorIs(t, err, os.ErrNotExist)
}
