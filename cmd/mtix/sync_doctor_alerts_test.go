// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests exercise operator alert projection through the existing doctor.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestRunSyncDoctor_OperatorAlerts_ReportsRecent(t *testing.T) {
	for _, tc := range []struct {
		name string
		json bool
	}{{"json", true}, {"text", false}} {
		t.Run(tc.name, func(t *testing.T) {
			state := alertDoctorFixture(t)
			seedDoctorAlert(t, state)
			app.jsonOutput = tc.json
			var out, errOut bytes.Buffer
			err := runSyncDoctor(context.Background(), &out, &errOut, nil, transport.Options{})
			require.ErrorIs(t, err, errDoctorChecksFailed, "unconfigured hub fails independently of local alerts")
			if tc.json {
				check := doctorCheckFromJSON(t, out.Bytes(), "alerts.recent")
				require.True(t, check.Pass)
				require.True(t, check.Warn)
				assertDoctorRecentAlert(t, out.Bytes())
				return
			}
			require.Contains(t, out.String(), "[WARN] alerts.recent")
			require.Contains(t, out.String(), "wake.hop-depth")
			require.Contains(t, out.String(), "test-agent")
			require.Contains(t, out.String(), "count 2")
			require.Contains(t, out.String(), "mtix config set wake.max_hop_depth 64")
		})
	}
}

func TestRunSyncDoctor_NoOperatorAlerts_PreservesLocalChecks(t *testing.T) {
	state := alertDoctorFixture(t)
	app.jsonOutput = true
	var out, errOut bytes.Buffer
	require.ErrorIs(t, runSyncDoctor(t.Context(), &out, &errOut, nil, transport.Options{}), errDoctorChecksFailed)
	check := doctorCheckFromJSON(t, out.Bytes(), "operator state placement")
	require.True(t, check.Pass)
	require.True(t, check.Warn)
	dir, err := state.Path()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	if err != nil {
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	for _, entry := range entries {
		require.False(t, strings.HasPrefix(entry.Name(), "alert"), "reading absent alert state creates no alert files: %s", entry.Name())
	}
}

func alertDoctorFixture(t *testing.T) *oplocal.State {
	t.Helper()
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "separate"))
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	return oplocal.New(env)
}

func seedDoctorAlert(t *testing.T, state *oplocal.State) {
	t.Helper()
	host, err := state.HostID()
	require.NoError(t, err)
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	row := map[string]any{
		"id": "0123456789abcdef0123456789abcdef", "host_id": host,
		"agent": "test-agent", "class": "wake.hop-depth", "count": 2,
		"first_at": at, "last_at": at.Add(time.Minute),
		"detail": "configured guard reported a recent occurrence",
		"fix":    "mtix config set wake.max_hop_depth 64",
	}
	data, err := json.Marshal(row)
	require.NoError(t, err)
	writeProtectedAlertFixture(t, state, "alerts-fixture", "alerts.jsonl", append(data, '\n'))
	writeProtectedAlertFixture(t, state, "alerts-lock-fixture", "alerts.lock", []byte{})
	status, err := state.Inspect()
	require.NoError(t, err)
	require.Equal(t, "ready", status.Status)
	again, err := state.HostID()
	require.NoError(t, err)
	require.Equal(t, host, again)
}

func writeProtectedAlertFixture(t *testing.T, state *oplocal.State, name, target string, data []byte) {
	t.Helper()
	require.NoError(t, state.WriteJSON(name, "protected fixture"))
	var probe string
	reports, err := state.ReadJSON(name, &probe)
	require.NoError(t, err)
	require.Empty(t, reports)
	require.Equal(t, "protected fixture", probe)
	dir, err := state.Path()
	require.NoError(t, err)
	source := filepath.Join(dir, name+".json")
	before, err := os.Stat(source)
	require.NoError(t, err)
	// Overwriting a pre-created inode preserves its existing native access rules.
	require.NoError(t, os.WriteFile(source, data, 0600))
	path := filepath.Join(dir, target)
	require.NoError(t, os.Rename(source, path))
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, actual)
}

func assertDoctorRecentAlert(t *testing.T, raw []byte) {
	t.Helper()
	var report struct {
		Checks []struct {
			Name   string `json:"name"`
			Recent []struct {
				ID    string `json:"id"`
				Agent string `json:"agent"`
				Class string `json:"class"`
				Count uint64 `json:"count"`
				Fix   string `json:"fix"`
			} `json:"recent"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(raw, &report))
	for _, check := range report.Checks {
		if check.Name != "alerts.recent" {
			continue
		}
		require.Len(t, check.Recent, 1)
		row := check.Recent[0]
		require.Equal(t, "0123456789abcdef0123456789abcdef", row.ID)
		require.Equal(t, "test-agent", row.Agent)
		require.Equal(t, "wake.hop-depth", row.Class)
		require.Equal(t, uint64(2), row.Count)
		require.Equal(t, "mtix config set wake.max_hop_depth 64", row.Fix)
		return
	}
	t.Fatal("doctor has no recent alert payload")
}
