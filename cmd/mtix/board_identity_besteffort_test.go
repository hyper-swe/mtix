// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Identity caching after an applied pull is best-effort; doctor reports a missing cache.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheIdentityBestEffort_FailureWarnsWithoutFailing(t *testing.T) {
	var stderr bytes.Buffer
	cacheIdentityBestEffort(&stderr, func() error { return errors.New("state unwritable") })
	assert.Contains(t, stderr.String(), "WARN")
	assert.Contains(t, stderr.String(), "state unwritable")
	stderr.Reset()
	cacheIdentityBestEffort(&stderr, func() error { return nil })
	assert.Empty(t, stderr.String())
}

func TestCacheIdentityBestEffort_ConflictedBoardID_WarnsAndKeepsObservation(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"),
		[]byte("0c6f03d9-3633-4ab7-bcc9-b8da8d966023\n4549495e-f331-4ac3-86af-decd9cbcbda6\n"), 0600))
	var stderr bytes.Buffer
	cacheIdentityBestEffort(&stderr, func() error {
		return cachePulledIdentityFrom(t.Context(), identityQuery{t: t})
	})
	assert.Contains(t, stderr.String(), model.ErrInvalidInput.Error())
	got, _, err := state.BoardObservation(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	assert.Empty(t, got.Hub.FirstEventHash)
}

func TestAppendBoardIdentityChecks_ConnectedBoardWithoutBoardID_WarnsAndPasses(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	require.NoError(t, state.ObserveBoard(filepath.Dir(app.mtixDir), "", oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}))
	report := appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.True(t, report.OverallPass, "an existing hub board cannot be failed for a file this build cannot create")
	require.NotEmpty(t, report.Checks)
	check := report.Checks[0]
	assert.Equal(t, "board.id", check.Name)
	assert.True(t, check.Warn)
	assert.Empty(t, check.Fix)
	assert.NotContains(t, check.Detail, "mtix setup")
}

func TestAppendIdentityChangeCheck_GrantForAnotherBoard_NoWarning(t *testing.T) {
	current := "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"
	other := "4549495e-f331-4ac3-86af-decd9cbcbda6"
	recorded := oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}
	observed := oplocal.BoardObservation{Hub: oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "second"}}
	report := appendIdentityChangeCheck(DoctorReport{OverallPass: true}, current, observed, []oplocal.Grant{{BoardID: other, Hub: recorded}})
	assert.Empty(t, report.Checks, "a confirmation for a different board says nothing about this board")
	report = appendIdentityChangeCheck(DoctorReport{OverallPass: true}, current, observed, []oplocal.Grant{{BoardID: current, Hub: recorded}})
	require.Len(t, report.Checks, 1)
	assert.Equal(t, "board.hub-identity-changed", report.Checks[0].Name)
}
