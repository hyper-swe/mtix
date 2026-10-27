// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Doctor exposes effective wake values and ignored workspace authority without writes.
package main

import (
	"bytes"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorWakeSettings_TextAndJSON_ReportsValuesSourcesAndIgnoredKey(t *testing.T) {
	initTestApp(t)
	state := testutil.OperatorState(t)
	require.NoError(t, state.WriteJSON("wake", map[string]any{"host": map[string]any{"wake.live": false}}))
	path := filepath.Join(app.mtixDir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("prefix: TEST\nwake:\n  max_hop_depth: 99\n"), 0600))
	var err error
	app.configSvc, err = service.NewConfigService(path)
	require.NoError(t, err)
	dir, err := state.Path()
	require.NoError(t, err)
	before, err := os.ReadFile(filepath.Join(dir, "wake.json"))
	require.NoError(t, err)
	for _, jsonOutput := range []bool{false, true} {
		svc, err := newConfiguredDoctorService()
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: jsonOutput, Fix: true}))
		require.Contains(t, out.String(), "wake.settings")
		require.Contains(t, out.String(), "settings.ignored-workspace-key")
		require.Contains(t, out.String(), "wake.max_hop_depth")
		require.Contains(t, out.String(), "mtix config set wake.max_hop_depth 99")
		if jsonOutput {
			var rows []service.DoctorResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
			for _, row := range rows {
				if row.ID == "wake.settings" {
					require.Contains(t, row.Data, "host")
					host := row.Data["host"].(map[string]any)
					live := host["wake.live"].(map[string]any)
					require.Equal(t, "false", live["value"])
					require.Equal(t, "host", live["source"])
					depth := host["wake.max_hop_depth"].(map[string]any)
					require.Equal(t, "32", depth["value"])
					require.Equal(t, "default", depth["source"])
				}
			}
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "wake.json"))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestIgnoredWorkspaceSettings_RemedyUsesCommittedValuesAndRemoveOnly(t *testing.T) {
	initTestApp(t)
	testutil.OperatorState(t)
	path := filepath.Join(app.mtixDir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("wake:\n  live: false\n  max_hop_depth: 17\n  wakes_per_hour: 12\n  mystery: secret-untrusted-value\n  backoff_after_unacked: broken\n"), 0600))
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)
	row := ignoredWorkspaceSettingsRow(cs)
	for _, command := range []string{"mtix config set wake.live false", "mtix config set wake.max_hop_depth 17", "mtix config set wake.wakes_per_hour 12"} {
		require.Contains(t, row.Fix, command)
	}
	require.NotContains(t, row.Fix, "mtix config set wake.live true")
	require.NotContains(t, row.Fix, "mtix config set wake.mystery")
	require.NotContains(t, row.Fix, "mtix config set wake.backoff_after_unacked")
	require.Contains(t, row.Fix, "Remove-only")
	require.Contains(t, row.Fix, "operator terminal")
	require.NotContains(t, row.Fix, "secret-untrusted-value")
}
