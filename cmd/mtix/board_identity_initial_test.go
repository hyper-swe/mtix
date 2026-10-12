// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Board identity checks use existing init and doctor command boundaries.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInit_NewBoard_CreatesCommittedIdentity(t *testing.T) {
	saveAndResetApp(t)
	t.Chdir(t.TempDir())
	app.logger = slog.Default()
	require.NoError(t, runInit("TEST"))
	raw, err := os.ReadFile(filepath.Join(".mtix", "board-id"))
	require.NoError(t, err)
	require.Len(t, raw, 37)
	id, err := uuid.Parse(string(raw[:36]))
	require.NoError(t, err)
	assert.Equal(t, id.String()+"\n", string(raw))
	require.Error(t, runInit("TEST"))
	again, err := os.ReadFile(filepath.Join(".mtix", "board-id"))
	require.NoError(t, err)
	assert.Equal(t, raw, again)
}

func identityDoctorReport(t *testing.T, raw string) DoctorReport {
	t.Helper()
	initTestApp(t)
	identityOperatorState(t)
	_, configErr := app.configSvc.Set("sync.enabled", "true")
	require.NoError(t, configErr)
	t.Setenv(transport.EnvDSN, "")
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(raw), 0600))
	app.jsonOutput = true
	var out, diagnostic bytes.Buffer
	require.ErrorIs(t, runSyncDoctor(t.Context(), &out, &diagnostic, nil, transport.Options{}), errDoctorChecksFailed)
	var report DoctorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report), out.String())
	return report
}

func TestDoctorBoardID_MissingHubRow_FailsWithSyncInitFix(t *testing.T) {
	report := identityDoctorReport(t, "0c6f03d9-3633-4ab7-bcc9-b8da8d966023\n")
	var found bool
	for _, check := range report.Checks {
		if check.Name == "board.id" {
			found = true
			assert.False(t, check.Pass)
			assert.Equal(t, "mtix sync init", check.Fix)
		}
	}
	assert.True(t, found, "existing doctor reports board identity")
}

func TestDoctorBoardID_TwoMintedIDs_Detected(t *testing.T) {
	report := identityDoctorReport(t, "<<<<<<< current\n0c6f03d9-3633-4ab7-bcc9-b8da8d966023\n=======\n4549495e-f331-4ac3-86af-decd9cbcbda6\n>>>>>>> incoming\n")
	var found bool
	for _, check := range report.Checks {
		if check.Name == "board.id" {
			found = true
			assert.False(t, check.Pass)
			assert.Contains(t, check.Detail, "multiple")
		}
	}
	assert.True(t, found, "existing doctor detects multiple identities")
}
