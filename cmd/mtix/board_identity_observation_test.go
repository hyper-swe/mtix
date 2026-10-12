// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Successful unknown observations differ from failed lookups.

import (
	"errors"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRecordPullObservation_KnownUnknownAndError_KeepDistinctOutcomes(t *testing.T) {
	state := identityOperatorState(t)
	checkout := t.TempDir()
	id := "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"
	known := func() (string, string, error) { return "TEST", "first", nil }
	require.NoError(t, recordPullObservation(state, checkout, id, known))
	before, _, err := state.BoardObservation(checkout)
	require.NoError(t, err)
	assert.True(t, before.Hub.Known())
	failure := errors.New("lookup refused")
	err = recordPullObservation(state, checkout, id, func() (string, string, error) { return "", "", failure })
	assert.ErrorIs(t, err, failure)
	after, _, err := state.BoardObservation(checkout)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	for _, row := range [][2]string{{"", ""}, {"TEST", ""}} {
		require.NoError(t, recordPullObservation(state, checkout, id, func() (string, string, error) { return row[0], row[1], nil }))
		observed, _, readErr := state.BoardObservation(checkout)
		require.NoError(t, readErr)
		assert.False(t, observed.Hub.Known())
		require.NoError(t, recordPullObservation(state, checkout, id, known))
	}
	var grants []oplocal.Grant
	grants, _, err = state.BoardGrants()
	require.NoError(t, err)
	assert.Empty(t, grants)
}
