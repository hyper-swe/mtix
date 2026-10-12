// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Local cache errors remain distinct from successful missing identity observations.

import (
	"bytes"
	"errors"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestCachePulledIdentity_LocalQuery_UsesValidatedObservationState(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	query := identityQuery{rows: &identityRows{found: true, prefix: "TEST", hash: "first"}, t: t}
	require.NoError(t, cachePulledIdentityFrom(t.Context(), query))
	got, _, err := state.BoardObservation(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	assert.Equal(t, "first", got.Hub.FirstEventHash)
	assert.Empty(t, got.BoardID)
	failure := errors.New("driver refused")
	query.err = failure
	err = cachePulledIdentityFrom(t.Context(), query)
	assert.ErrorIs(t, err, failure)
	after, _, err := state.BoardObservation(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	assert.Equal(t, got, after)
}

func TestExistingBoardIdentity_MalformedOrMultiple_ReturnsRefusal(t *testing.T) {
	initTestApp(t)
	for _, raw := range []string{"invalid\n", "0c6f03d9-3633-4ab7-bcc9-b8da8d966023\n4549495e-f331-4ac3-86af-decd9cbcbda6\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(raw), 0600))
		_, err := existingBoardIdentity()
		assert.ErrorIs(t, err, model.ErrInvalidInput)
		err = cachePulledIdentityFrom(t.Context(), identityQuery{t: t})
		assert.ErrorIs(t, err, model.ErrInvalidInput)
		err = cacheVerifiedRelay("relay", "019b8e44-42b3-7000-8000-000000000001")
		assert.ErrorIs(t, err, model.ErrInvalidInput)
	}
}

func TestRecordPullObservation_UnreadableState_PreservesValidationCause(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	path := t.TempDir()
	failure := recordPullObservation(state, path, "invalid", func() (string, string, error) { return "TEST", "first", nil })
	assert.ErrorIs(t, failure, model.ErrOperatorStateUnreadable)
	var out bytes.Buffer
	assert.ErrorIs(t, wrapSyncErr(&out, "identity", failure), model.ErrOperatorStateUnreadable)
}

func TestCacheVerifiedRelay_UnreadableOperatorDirectory_ReturnsRefusal(t *testing.T) {
	initTestApp(t)
	identityOperatorState(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := cacheVerifiedRelay("relay", "019b8e44-42b3-7000-8000-000000000001")
	assert.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestAppendBoardIdentityChecks_InvalidAndAbsentState_RemainReadOnly(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	report := appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.True(t, report.OverallPass)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte("invalid\n"), 0600))
	report = appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.False(t, report.OverallPass)
	require.NoError(t, state.WriteJSON("grants", []string{"invalid"}))
	report = appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.False(t, report.OverallPass)
	require.NoError(t, state.WriteJSON("grants", map[string]any{}))
	require.NoError(t, state.WriteJSON("wake", []string{"invalid"}))
	report = appendBoardIdentityChecks(DoctorReport{OverallPass: true})
	assert.False(t, report.OverallPass)
}
