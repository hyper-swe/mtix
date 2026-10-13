// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Identity consistency checks use only recorded local observations.

import (
	"crypto/sha256"
	"encoding/base32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendBoardIdentityChecks_RecordedIdentity_ReportsConsistency(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	id := "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"
	other := "4549495e-f331-4ac3-86af-decd9cbcbda6"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(id+"\n"), 0600))
	hub := oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}
	checkout := filepath.Dir(app.mtixDir)
	require.NoError(t, state.ObserveBoard(checkout, id, hub))
	report := appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.True(t, report.OverallPass)
	require.NoError(t, state.WriteJSON("grants", map[string]any{"board_grants": []oplocal.Grant{{BoardID: id, Hub: hub}}}))
	report = appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.True(t, report.OverallPass)
	require.NoError(t, state.ObserveBoard(checkout, other, hub))
	report = appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.False(t, report.OverallPass)
	assert.Contains(t, findFailingCheck(t, report).Detail, "multiple board identities")
}

func TestAppendBoardIdentityChecks_RelayConfigurationChanged_ReportsUnknown(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	id := "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(id+"\n"), 0600))
	relay := filepath.Join(filepath.Dir(app.mtixDir), "relay")
	_, err := app.configSvc.Set("sync.relay.dir", relay)
	require.NoError(t, err)
	require.NoError(t, state.ObserveRelay(filepath.Dir(app.mtixDir), id, relay, "019b8e44-42b3-7000-8000-000000000001"))
	assert.True(t, appendBoardIdentityChecks(DoctorReport{OverallPass: true}).OverallPass)
	_, err = app.configSvc.Set("sync.relay.dir", filepath.Join(relay, "next"))
	require.NoError(t, err)
	report := appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.False(t, report.OverallPass)
	assert.Equal(t, "mtix sync init", findFailingCheck(t, report).Fix)
	observed, _, err := state.BoardObservation(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	assert.Equal(t, relay, observed.RelayPath)
}

func TestAppendBoardIdentityChecks_InvalidObservation_ReportsLocalRemedy(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	checkout := filepath.Dir(app.mtixDir)
	sum := sha256.Sum256([]byte(checkout))
	name := "identity-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
	require.NoError(t, state.WriteJSON(name, map[string]any{"checkout": "wrong"}))
	report := appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.False(t, report.OverallPass)
	assert.Equal(t, "mtix hooks status --json", report.Checks[0].Fix)
}

func TestAppendBoardIdentityChecks_NoConfiguration_RemainsInformational(t *testing.T) {
	initTestApp(t)
	identityOperatorState(t)
	saved := app.configSvc
	app.configSvc = nil
	t.Cleanup(func() { app.configSvc = saved })
	report := appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.True(t, report.OverallPass)
	assert.Contains(t, report.Checks[0].Detail, "not required")
}
