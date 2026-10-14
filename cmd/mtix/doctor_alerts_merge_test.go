// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

const doctorMergeSyntheticDSN = "postgres://fixture:integration_alert_secret@/%zz"

func doctorMergeProtectedFile(t *testing.T, state *oplocal.State, name string, data []byte) {
	t.Helper()
	require.NoError(t, state.WriteJSON("merge-fixture", "protected fixture"))
	dir, err := state.Path()
	require.NoError(t, err)
	source := filepath.Join(dir, "merge-fixture.json")
	before, err := os.Stat(source)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(source, data, 0o600))
	target := filepath.Join(dir, name)
	require.NoError(t, os.Rename(source, target))
	after, err := os.Stat(target)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "retain protected native inode access rules")
}

func doctorMergeSeedAlert(t *testing.T) string {
	t.Helper()
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	state := oplocal.New(env)
	host, err := state.HostID()
	require.NoError(t, err)
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	row := map[string]any{
		"id": "0123456789abcdef0123456789abcdef", "host_id": host,
		"agent": doctorMergeSyntheticDSN, "class": "wake.hop-depth", "count": 2,
		"first_at": at, "last_at": at.Add(time.Minute),
		"detail": "recent alert " + doctorMergeSyntheticDSN,
		"fix":    "remedy " + doctorMergeSyntheticDSN,
	}
	data, err := json.Marshal(row)
	require.NoError(t, err)
	doctorMergeProtectedFile(t, state, "alerts.jsonl", append(data, '\n'))
	doctorMergeProtectedFile(t, state, "alerts.lock", []byte{})
	return host
}

func doctorMergeAssertTopAlerts(t *testing.T, body string) {
	t.Helper()
	rows := doctorCLIRows(t, body)
	found := 0
	for _, row := range rows {
		if row["id"] != "sync.alerts.recent" {
			continue
		}
		found++
		assert.Equal(t, "warn", row["status"])
		assert.Contains(t, row["detail"], "wake.hop-depth")
		assert.Contains(t, row["detail"], "count 2")
		assert.Contains(t, row["detail"], "fix: remedy")
		assert.Contains(t, row["detail"], "REDACTED")
	}
	assert.Equal(t, 1, found, "retain exactly one flattened Alerts row and four-field schema")
}

func doctorMergeAssertRecentDTO(t *testing.T, recent []map[string]any, host string) {
	t.Helper()
	require.Len(t, recent, 1)
	row := recent[0]
	assert.Len(t, row, 9, "preserve the complete upstream Recent DTO")
	assert.Equal(t, "0123456789abcdef0123456789abcdef", row["id"])
	assert.Equal(t, host, row["host_id"])
	assert.Equal(t, "wake.hop-depth", row["class"])
	assert.Equal(t, float64(2), row["count"])
	assert.Equal(t, "2026-10-12T12:00:00Z", row["first_at"])
	assert.Equal(t, "2026-10-12T12:01:00Z", row["last_at"])
	for _, key := range []string{"agent", "detail", "fix"} {
		assert.Contains(t, row[key], "REDACTED")
	}
}

func doctorMergeAssertLegacyAlerts(t *testing.T, body, host string) {
	t.Helper()
	var report struct {
		Pass   bool `json:"pass"`
		Checks []struct {
			Name   string           `json:"name"`
			Pass   bool             `json:"pass"`
			Warn   bool             `json:"warn"`
			Recent []map[string]any `json:"recent"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &report))
	assert.False(t, report.Pass, "malformed synthetic hub fails independently of local Alerts")
	require.GreaterOrEqual(t, len(report.Checks), 2)
	assert.Equal(t, "PG reachable", report.Checks[0].Name)
	assert.Equal(t, "schema current", report.Checks[1].Name)
	found := 0
	for _, check := range report.Checks {
		if check.Name == "alerts.recent" {
			found++
			assert.True(t, check.Pass)
			assert.True(t, check.Warn)
			doctorMergeAssertRecentDTO(t, check.Recent, host)
		}
	}
	assert.Equal(t, 1, found, "shared collection must retain upstream Alerts exactly once")
}

func TestDoctorCLI_MergeAlerts_ConfiguredTopAndLegacy(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "top", true: "legacy"}[legacy], map[bool]string{false: "text", true: "json"}[jsonOutput]}, "-"), func(t *testing.T) {
				root := doctorCLIBoard(t, "")
				host := doctorMergeSeedAlert(t)
				args := []string{"doctor"}
				name := "sync.alerts.recent"
				if legacy {
					args = []string{"sync", "doctor"}
					name = "alerts.recent"
				}
				if jsonOutput {
					args = append(args, "--json")
				}
				out := doctorCLIExecute(t, root, doctorMergeSyntheticDSN, args...)
				assert.Equal(t, 2, out.exit, out.stderr)
				assert.Empty(t, out.stderr, "both health failures stay quiet after printing their reports")
				assert.NotContains(t, out.stdout+out.stderr, "integration_alert_secret")
				if jsonOutput && legacy {
					doctorMergeAssertLegacyAlerts(t, out.stdout, host)
				} else if jsonOutput {
					doctorMergeAssertTopAlerts(t, out.stdout)
				} else {
					assert.Contains(t, out.stdout, "[WARN] "+name)
					assert.Contains(t, out.stdout, "wake.hop-depth")
					assert.Contains(t, out.stdout, "count 2")
					assert.Contains(t, out.stdout, "REDACTED")
				}
			})
		}
	}
}
