// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Wake config commands persist authority only in operator-local host state.
package main

import (
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigWake_AgentFlag_PersistsOnlyOperatorState(t *testing.T) {
	initTestApp(t)
	state := testutil.OperatorState(t)
	path := filepath.Join(app.mtixDir, "config.yaml")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	cmd := newConfigSetCmd()
	cmd.SetArgs([]string{"wake.live", "off", "--agent", "worker"})
	require.NoError(t, cmd.Execute())
	values, _, err := state.WakeSettings("worker")
	require.NoError(t, err)
	require.Equal(t, "false", values["wake.live"].Value)
	host, _, err := state.WakeSettings("")
	require.NoError(t, err)
	require.Equal(t, "true", host["wake.live"].Value)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
