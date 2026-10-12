// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Only a successful relay attach may record the verified local identity.

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestRelayAttach_VerifiedIdentity_PersistsOnlyAfterSuccess(t *testing.T) {
	dir := initCommandRelay(t, true)
	state := identityOperatorState(t)
	id, err := service.CreateBoardID(app.mtixDir)
	require.NoError(t, err)
	doc, err := metadata.Read(dir)
	require.NoError(t, err)
	_, _, err = relayCommandOutput(newRelayAttachCmd(), dir)
	require.NoError(t, err)
	checkout := filepath.Dir(app.mtixDir)
	before, reports, err := state.BoardObservation(checkout)
	require.NoError(t, err)
	require.Empty(t, reports)
	assert.Equal(t, dir, before.RelayPath)
	assert.Equal(t, id, before.BoardID)
	assert.Equal(t, "relay", before.Hub.Kind)
	assert.Equal(t, doc.RelayID, before.Hub.RelayID)
	require.NoError(t, os.RemoveAll(relayKeysDir()))
	_, _, err = relayCommandOutput(newRelayAttachCmd(), dir)
	require.Error(t, err)
	after, _, err := state.BoardObservation(checkout)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	grants, _, err := state.BoardGrants()
	require.NoError(t, err)
	assert.Empty(t, grants)
}

func TestRelayAttach_UnreadableOperatorState_ReturnsError(t *testing.T) {
	dir := initCommandRelay(t, true)
	identityOperatorState(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, err := relayCommandOutput(newRelayAttachCmd(), dir)
	assert.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}
