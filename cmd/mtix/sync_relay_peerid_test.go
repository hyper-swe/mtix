// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
)

// withConfig installs a temporary config service for the duration of a
// test, restoring whatever was there.
func withConfig(t *testing.T) *service.ConfigService {
	t.Helper()
	cs, err := service.NewConfigService(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)

	saved := app.configSvc
	app.configSvc = cs
	t.Cleanup(func() { app.configSvc = saved })
	return cs
}

// TestRelayPeerID_ConfiguredIdentityWins is FR-21 §7.1. A peer whose
// workspace is rebuilt somewhere else between sessions needs an
// identity that outlives the machine: a machine-derived id would change
// with the machine, and the peer would rejoin as a NEW member each
// time, leaving the abandoned one holding everyone's retention window
// open until an operator retired it.
func TestRelayPeerID_ConfiguredIdentityWins(t *testing.T) {
	cs := withConfig(t)
	_, err := cs.Set("sync.relay.peer_id", "0123456789abcdef-desk")
	require.NoError(t, err)

	got, err := relayPeerID()
	require.NoError(t, err)
	require.Equal(t, "0123456789abcdef-desk", got)
}

// TestRelayPeerID_DerivesFromTheMachineWhenUnset is the default, and it
// is the right one whenever the machine is the thing that persists.
func TestRelayPeerID_DerivesFromTheMachineWhenUnset(t *testing.T) {
	withConfig(t)

	got, err := relayPeerID()
	require.NoError(t, err)
	require.NoError(t, segment.ValidatePeerID(got),
		"a machine-derived id must satisfy the same grammar as a configured one")

	again, err := relayPeerID()
	require.NoError(t, err)
	require.Equal(t, got, again, "the derived identity is stable within a machine")
}

// TestRelayPeerID_RefusesAHandEditedMalformedIdentity covers the path
// `config set` cannot guard: a config file edited directly. A malformed
// id would otherwise mint a peer directory nothing else recognises —
// every other peer would ignore it as a foreign entry while this one
// believed it was publishing.
func TestRelayPeerID_RefusesAHandEditedMalformedIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"sync:\n  relay.peer_id: NOT-A-PEER-ID\n"), 0o600))

	cs, err := service.NewConfigService(path)
	require.NoError(t, err)
	saved := app.configSvc
	app.configSvc = cs
	t.Cleanup(func() { app.configSvc = saved })

	got, err := cs.Get("sync.relay.peer_id")
	require.NoError(t, err)
	require.Equal(t, "NOT-A-PEER-ID", got, "the file was loaded without Set-time validation")

	_, err = relayPeerID()
	require.Error(t, err)
	require.Contains(t, err.Error(), "malformed")
}
