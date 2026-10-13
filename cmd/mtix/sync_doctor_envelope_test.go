// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Envelope configuration diagnostics use the real doctor text and JSON APIs.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestSyncDoctorEnvelope_InvalidCaps_ReportsExactRemedy(t *testing.T) {
	cases := []struct {
		name string
		caps map[string]any
	}{
		{"negative", map[string]any{"max_events": -1}},
		{"framing too small", map[string]any{"max_total_bytes": 1}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			envelopeWakeConfig(t, tt.caps)
			t.Setenv(transport.EnvDSN, "")
			var out, errOut bytes.Buffer
			app.jsonOutput = true
			require.ErrorIs(t, runSyncDoctor(context.Background(), &out, &errOut, nil, transport.Options{}), errDoctorChecksFailed)
			check := doctorCheckFromJSON(t, out.Bytes(), "inbox envelope caps")
			assert.False(t, check.Pass)
			assert.Contains(t, check.Fix, "wake.json")
			assert.Contains(t, check.Fix, "0")
			app.jsonOutput = false
			out.Reset()
			require.ErrorIs(t, runSyncDoctor(context.Background(), &out, &errOut, nil, transport.Options{}), errDoctorChecksFailed)
			assert.Contains(t, out.String(), "[FAIL] inbox envelope caps")
			assert.Contains(t, out.String(), check.Fix)
		})
	}
}

func TestSyncDoctorEnvelope_DefaultsAndHomeFailure_ReportsLocally(t *testing.T) {
	initTestApp(t)
	report := appendEnvelopeCapsCheck(DoctorReport{OverallPass: true})
	require.Len(t, report.Checks, 1)
	assert.True(t, report.Checks[0].Pass)
	assert.Contains(t, report.Checks[0].Detail, "max_events=0")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	report = appendEnvelopeCapsCheck(DoctorReport{OverallPass: true})
	require.Len(t, report.Checks, 1)
	assert.False(t, report.Checks[0].Pass)
	assert.Contains(t, report.Checks[0].Fix, "mtix sync doctor --json")
	_, err := loadInboxEnvelopeCaps()
	require.Error(t, err)
}

func TestSyncDoctorEnvelope_ForeignHost_RequiresFreshLocalRecord(t *testing.T) {
	initTestApp(t)
	envelopeWakeConfig(t, map[string]any{"max_events": 3, "foreign_setting": "do not adopt"})
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	state := oplocal.New(env)
	id, err := state.HostID()
	require.NoError(t, err)
	other := strings.Repeat("0", 32)
	if other == id {
		other = strings.Repeat("1", 32)
	}
	path, err := state.Path()
	require.NoError(t, err)
	file := filepath.Join(path, "wake.json")
	raw, err := json.Marshal(map[string]any{"host_id": other, "data": map[string]any{"max_events": 3, "foreign_setting": "do not adopt"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, raw, 0600))
	report := appendEnvelopeCapsCheck(DoctorReport{OverallPass: true})
	require.Len(t, report.Checks, 1)
	assert.False(t, report.Checks[0].Pass)
	fix := report.Checks[0].Fix
	for _, phrase := range []string{"Move ", "private backup", "do not adopt foreign settings", "host-id", "0600", "user-only ACL"} {
		assert.Contains(t, fix, phrase)
	}
	_, err = loadInboxEnvelopeCaps()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not adopt foreign settings")
	require.NoError(t, os.Rename(file, file+".foreign-backup"))
	require.NoError(t, state.WriteJSON("wake", map[string]int{"max_events": 0, "max_body_bytes": 0, "max_total_bytes": 0}))
	report = appendEnvelopeCapsCheck(DoctorReport{OverallPass: true})
	assert.True(t, report.Checks[0].Pass)
	var local map[string]any
	_, err = state.ReadJSON("wake", &local)
	require.NoError(t, err)
	assert.NotContains(t, local, "foreign_setting")
}
