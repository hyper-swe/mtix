// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Existing sync doctor reports identity observations without altering confirmations.

import (
	"bytes"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorHubIdentityChanged_Warns(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	t.Setenv(transport.EnvDSN, "")
	id := "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(id+"\n"), 0600))
	original := oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}
	observed := original
	observed.FirstEventHash = "second"
	require.NoError(t, state.WriteJSON("grants", map[string]any{"board_grants": []oplocal.Grant{{BoardID: id, Hub: original}}}))
	require.NoError(t, state.ObserveBoard(filepath.Dir(app.mtixDir), id, observed))
	var out bytes.Buffer
	app.jsonOutput = true
	require.ErrorIs(t, runSyncDoctor(t.Context(), &out, nil, transport.Options{}), errDoctorChecksFailed)
	var report DoctorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	found := false
	for _, check := range report.Checks {
		if check.Name == "board.hub-identity-changed" {
			found = true
			assert.True(t, check.Pass)
			assert.True(t, check.Warn)
			assert.Empty(t, check.Fix, "no command in this build re-confirms a hub")
			assert.NotContains(t, check.Detail, "mtix setup")
			assert.NotContains(t, check.Detail, "mtix config set")
		}
	}
	assert.True(t, found, "identity changes are reported")
	app.jsonOutput = false
	out.Reset()
	require.ErrorIs(t, runSyncDoctor(t.Context(), &out, nil, transport.Options{}), errDoctorChecksFailed)
	assert.Contains(t, out.String(), "[WARN] board.hub-identity-changed")
	assert.NotContains(t, out.String(), "mtix setup")
	assert.NotContains(t, out.String(), "mtix config set")
	grants, _, err := state.BoardGrants()
	require.NoError(t, err)
	assert.Equal(t, original, grants[0].Hub)
}
