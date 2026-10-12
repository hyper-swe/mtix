// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

// Host-local observations and settings retain the existing validated record envelope.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func identityState(t *testing.T) *State {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	root := safeFixtureRoot(t)
	return New(Env{GOOS: runtime.GOOS, Home: home, Values: map[string]string{"XDG_CONFIG_HOME": root, "APPDATA": root}})
}

func TestObserveBoard_KnownThenUnknown_ClearsCurrentIdentity(t *testing.T) {
	s := identityState(t)
	path := t.TempDir()
	hub := HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}
	require.NoError(t, s.ObserveBoard(path, testBoardID, hub))
	got, reports, err := s.BoardObservation(path)
	require.NoError(t, err)
	require.Empty(t, reports)
	assert.Equal(t, hub, got.Hub)
	require.NoError(t, s.ObserveBoard(path, testBoardID, HubIdentity{}))
	got, reports, err = s.BoardObservation(path)
	require.NoError(t, err)
	require.Empty(t, reports)
	assert.Equal(t, path, got.Checkout)
	assert.Equal(t, testBoardID, got.BoardID)
	assert.Equal(t, HubIdentity{}, got.Hub)
	other, _, err := s.BoardObservation(filepath.Join(path, "other"))
	require.NoError(t, err)
	assert.Empty(t, other.Checkout)
}

func TestStrictBoardIdentity_DefaultAndHostSetting_AreReadOnly(t *testing.T) {
	s := identityState(t)
	strict, report, err := s.StrictBoardIdentity()
	require.NoError(t, err)
	require.Empty(t, report)
	assert.False(t, strict)
	require.NoError(t, s.WriteJSON("wake", map[string]any{"inbox.strict_board_identity": true, "retained": "value"}))
	strict, report, err = s.StrictBoardIdentity()
	require.NoError(t, err)
	require.Empty(t, report)
	assert.True(t, strict)
	var after map[string]any
	_, err = s.ReadJSON("wake", &after)
	require.NoError(t, err)
	assert.Equal(t, "value", after["retained"])
	require.NoError(t, s.WriteJSON("wake", map[string]any{"inbox.strict_board_identity": "true"}))
	_, _, err = s.StrictBoardIdentity()
	assert.Error(t, err)
}

func TestBoardGrants_ExistingConfirmations_ReadWithoutChangingState(t *testing.T) {
	s := identityState(t)
	grants := []Grant{{BoardID: testBoardID, Hub: HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}}}
	require.NoError(t, s.WriteJSON("grants", map[string]any{"board_grants": grants}))
	got, reports, err := s.BoardGrants()
	require.NoError(t, err)
	require.Empty(t, reports)
	assert.Equal(t, grants, got)
}
