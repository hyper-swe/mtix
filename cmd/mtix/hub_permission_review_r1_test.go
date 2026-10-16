// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewR1_PermissionFailureDoesNotBlockHubMirror(t *testing.T) {
	initTestApp(t)
	state := identityOperatorState(t)
	_, err := app.store.MarkHubConnected(context.Background(), "config")
	require.NoError(t, err)
	ids := "11111111-1111-4111-8111-111111111111\n22222222-2222-4222-8222-222222222222\n"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(ids), 0600))
	require.Error(t, reconcileHubMarkMirror(), "retain permission-observation failure")
	mirror, reports, err := state.HubMarkMirror(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	require.Empty(t, reports)
	assert.Equal(t, "config", mirror.Source, "independent mirror still follows the authoritative store")
}
