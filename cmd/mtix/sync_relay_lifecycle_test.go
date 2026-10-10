// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests relay initialization, attach refusals, authentication configuration,
// rotation boundaries and retirement using owned stores and generated keys.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/service"
)

// relayCommandOutput keeps operator output separate from error diagnostics.
func relayCommandOutput(cmd *cobra.Command, args ...string) (string, string, error) {
	var out, diagnostic bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostic)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), diagnostic.String(), err
}

// initCommandRelay uses the local history anchor, so successful attach is real.
func initCommandRelay(t *testing.T, authenticated bool) string {
	t.Helper()
	initTestApp(t)
	setDiagnosticConfig(t, "sync.relay.peer_id", diagnosticSelf)
	require.NoError(t, runCreate("Relay command history", "", "", 3, "", "", "", "", ""))
	dir := t.TempDir()
	args := []string{dir}
	if !authenticated {
		args = append(args, "--no-auth")
	}
	_, _, err := relayCommandOutput(newRelayInitCmd(), args...)
	require.NoError(t, err)
	setDiagnosticConfig(t, "sync.relay.dir", dir)
	return dir
}

func TestRelayInit_AuthenticationModes_PersistHistoryAndKeys(t *testing.T) {
	for _, authenticated := range []bool{true, false} {
		name := "authenticated"
		if !authenticated {
			name = "unauthenticated"
		}
		t.Run(name, func(t *testing.T) {
			initTestApp(t)
			setDiagnosticConfig(t, "sync.relay.peer_id", diagnosticSelf)
			require.NoError(t, runCreate("Initial relay history", "", "", 3, "", "", "", "", ""))
			prefix, hash, err := app.store.GetOrComputeLocalFirstEventHash(t.Context())
			require.NoError(t, err)
			dir := t.TempDir()
			args := []string{dir}
			if !authenticated {
				args = append(args, "--no-auth")
			}
			out, _, err := relayCommandOutput(newRelayInitCmd(), args...)
			require.NoError(t, err)
			doc, err := metadata.Read(dir)
			require.NoError(t, err)
			require.Equal(t, authenticated, doc.Authenticated)
			require.Equal(t, diagnosticSelf, doc.CreatedBy)
			require.Equal(t, []metadata.Project{{Prefix: prefix, FirstEventHash: hash}}, doc.Projects)
			require.NotEmpty(t, doc.RelayID)
			require.False(t, doc.CreatedAt.IsZero())
			require.Contains(t, out, "relay initialized at "+dir+" for project TEST")
			require.Contains(t, out, "next: mtix config set sync.relay.dir "+dir)
			assertRelayInitKeyMode(t, authenticated, doc, out)
		})
	}
}

func TestRelayInit_Refusals_DoNotCreateMetadata(t *testing.T) {
	for _, scenario := range []string{"empty history", "closed store", "malformed peer", "blocked directory"} {
		t.Run(scenario, func(t *testing.T) {
			initTestApp(t)
			setDiagnosticConfig(t, "sync.relay.peer_id", diagnosticSelf)
			dir := filepath.Join(t.TempDir(), "relay")
			want := "no project history"
			switch scenario {
			case "closed store":
				require.NoError(t, app.store.Close())
				want = "read local project history"
			case "malformed peer":
				path := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte("sync.relay.peer_id: invalid\n"), 0o600))
				var err error
				app.configSvc, err = service.NewConfigService(path)
				require.NoError(t, err)
				want = "is malformed"
			case "blocked directory":
				require.NoError(t, runCreate("Blocked relay initialization", "", "", 3, "", "", "", "", ""))
				require.NoError(t, os.WriteFile(dir, []byte("owned obstruction"), 0o600))
				want = "lstat"
			}
			out, _, err := relayCommandOutput(newRelayInitCmd(), dir)
			require.ErrorContains(t, err, want)
			require.NotContains(t, out, "relay initialized")
			_, statErr := os.Stat(filepath.Join(dir, metadata.FileName))
			require.Error(t, statErr)
		})
	}
}

func TestRelayInit_ExistingRelay_LeavesIdentityAndKeyUnchanged(t *testing.T) {
	dir := initCommandRelay(t, true)
	before, err := os.ReadFile(filepath.Join(dir, metadata.FileName))
	require.NoError(t, err)
	key, err := os.ReadFile(filepath.Join(relayKeysDir(), "1"))
	require.NoError(t, err)
	_, _, err = relayCommandOutput(newRelayInitCmd(), dir)
	require.ErrorIs(t, err, metadata.ErrRelayExists)
	after, err := os.ReadFile(filepath.Join(dir, metadata.FileName))
	require.NoError(t, err)
	require.Equal(t, before, after)
	afterKey, err := os.ReadFile(filepath.Join(relayKeysDir(), "1"))
	require.NoError(t, err)
	require.Equal(t, key, afterKey)
}

func TestRelayAttach_MatchingModeAndHistory_ReportsSharedProject(t *testing.T) {
	for _, authenticated := range []bool{true, false} {
		t.Run(map[bool]string{true: "authenticated", false: "unauthenticated"}[authenticated], func(t *testing.T) {
			dir := initCommandRelay(t, authenticated)
			setDiagnosticConfig(t, "sync.relay.require_auth", map[bool]string{true: "true", false: "false"}[authenticated])
			doc, err := metadata.Read(dir)
			require.NoError(t, err)
			out, _, err := relayCommandOutput(newRelayAttachCmd(), dir)
			require.NoError(t, err)
			require.Equal(t, "attached to relay "+doc.RelayID+"\nshared projects: [TEST]\nnext: mtix config set sync.relay.dir "+dir+"\n", out)
		})
	}
}

func TestRelayAttach_Refusals_PreserveErrorAndNameRecovery(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
		hint     string
	}{
		{"mode mismatch", lifecycle.ErrModeMismatch, "set sync.relay.require_auth"},
		{"history diverged", lifecycle.ErrHistoryDiverged, "different histories under one prefix"},
		{"no shared project", lifecycle.ErrNoSharedProject, "init your own relay"},
		{"missing key", keyring.ErrKeyAbsent, "copy the relay key into"},
		{"missing metadata", metadata.ErrRelayAbsent, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initCommandRelay(t, true)
			doc, err := metadata.Read(dir)
			require.NoError(t, err)
			switch tt.name {
			case "mode mismatch":
				setDiagnosticConfig(t, "sync.relay.require_auth", "false")
			case "history diverged":
				doc.Projects[0].FirstEventHash = "other-history"
				writeRelayFixtureMetadata(t, dir, doc)
			case "no shared project":
				doc.Projects[0].Prefix = "OTHER"
				writeRelayFixtureMetadata(t, dir, doc)
			case "missing key":
				require.NoError(t, os.Remove(filepath.Join(relayKeysDir(), "1")))
			case "missing metadata":
				require.NoError(t, os.Remove(filepath.Join(dir, metadata.FileName)))
			}
			out, _, err := relayCommandOutput(newRelayAttachCmd(), dir)
			require.ErrorIs(t, err, tt.sentinel)
			if tt.hint != "" {
				require.ErrorContains(t, err, tt.hint)
			}
			require.NotContains(t, out, "attached to relay")
		})
	}
}

func TestRelayAttach_ClosedStore_ExplainsHistoryRead(t *testing.T) {
	dir := initCommandRelay(t, true)
	require.NoError(t, app.store.Close())
	_, _, err := relayCommandOutput(newRelayAttachCmd(), dir)
	require.ErrorContains(t, err, "read local project history")
}

func TestRelayAttachHint_UnknownError_PreservesIdentity(t *testing.T) {
	err := errors.New("owned fixture error")
	require.Same(t, err, relayAttachHint(err))
}

func TestRelayRequireAuth_Configuration_IsExplicitOptOut(t *testing.T) {
	saveAndResetApp(t)
	require.True(t, relayRequireAuth())
	initTestApp(t)
	require.True(t, relayRequireAuth())
	for _, value := range []string{"false", "true"} {
		setDiagnosticConfig(t, "sync.relay.require_auth", value)
		require.Equal(t, value != "false", relayRequireAuth())
	}
	require.Equal(t, 37, relayConfigInt("unknown.fixture.setting", 37))
}

func TestRelayRotateKey_PublishedBoundary_PreservesOldKey(t *testing.T) {
	dir := initCommandRelay(t, true)
	_, _, err := relayCommandOutput(newRelayTickCmd())
	require.NoError(t, err)
	pos, err := app.store.RelayPushCursor(t.Context())
	require.NoError(t, err)
	old, err := os.ReadFile(filepath.Join(relayKeysDir(), "1"))
	require.NoError(t, err)
	out, _, err := relayCommandOutput(newRelayRotateKeyCmd())
	require.NoError(t, err)
	doc, err := metadata.Read(dir)
	require.NoError(t, err)
	epoch, ok := doc.CurrentKeyEpoch()
	require.True(t, ok)
	require.Equal(t, uint16(2), epoch)
	require.Equal(t, pos.NextRS, doc.KeyEpochs[1].FromRSByPeer[diagnosticSelf])
	after, err := os.ReadFile(filepath.Join(relayKeysDir(), "1"))
	require.NoError(t, err)
	require.Equal(t, old, after)
	ring, err := keyring.Load(relayKeysDir())
	require.NoError(t, err)
	_, err = ring.For(2)
	require.NoError(t, err)
	require.Contains(t, out, "key epoch 2 installed")
	require.Contains(t, out, "Keep the OLD epoch installed")
}

func TestRelayRotateKey_Refusals_LeaveEpochUnchanged(t *testing.T) {
	for _, scenario := range []string{"unconfigured", "cancelled context", "closed store", "unauthenticated"} {
		t.Run(scenario, func(t *testing.T) {
			dir := initCommandRelay(t, scenario != "unauthenticated")
			switch scenario {
			case "unconfigured":
				setDiagnosticConfig(t, "sync.relay.dir", "")
			case "closed store":
				require.NoError(t, app.store.Close())
			case "cancelled context":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				cmd := newRelayRotateKeyCmd()
				cmd.SetContext(ctx)
				_, _, err := relayCommandOutput(cmd)
				require.ErrorIs(t, err, context.Canceled)
				return
			}
			_, _, err := relayCommandOutput(newRelayRotateKeyCmd())
			require.Error(t, err)
			doc, readErr := metadata.Read(dir)
			require.NoError(t, readErr)
			require.LessOrEqual(t, len(doc.KeyEpochs), 1)
		})
	}
}

func TestRelayRetirePeer_RepeatedRetirement_PersistsOnce(t *testing.T) {
	dir := initCommandRelay(t, true)
	for range 2 {
		out, _, err := relayCommandOutput(newRelayRetirePeerCmd(), diagnosticOther)
		require.NoError(t, err)
		require.Contains(t, out, diagnosticOther+" retired; it no longer holds the prune window.")
		doc, err := metadata.Read(dir)
		require.NoError(t, err)
		require.Equal(t, []string{diagnosticOther}, doc.RetiredPeers)
	}
	for _, peer := range []string{"invalid", ""} {
		_, _, err := relayCommandOutput(newRelayRetirePeerCmd(), peer)
		require.ErrorContains(t, err, "malformed")
	}
	setDiagnosticConfig(t, "sync.relay.dir", "")
	_, _, err := relayCommandOutput(newRelayRetirePeerCmd(), diagnosticOther)
	require.ErrorContains(t, err, "no relay configured")
}

// writeRelayFixtureMetadata supplies a deliberately different attach identity.
func writeRelayFixtureMetadata(t *testing.T, dir string, doc *metadata.Relay) {
	t.Helper()
	body, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, metadata.FileName), body, 0o644))
}

func assertRelayInitKeyMode(t *testing.T, authenticated bool, doc *metadata.Relay, out string) {
	t.Helper()
	if authenticated {
		ring, loadErr := keyring.Load(relayKeysDir())
		require.NoError(t, loadErr)
		key, keyErr := ring.For(1)
		require.NoError(t, keyErr)
		require.Len(t, key, 32)
		info, statErr := os.Stat(filepath.Join(relayKeysDir(), "1"))
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		require.Contains(t, out, "key epoch 1 written to "+relayKeysDir())
	} else {
		_, statErr := os.Stat(relayKeysDir())
		require.True(t, os.IsNotExist(statErr))
		require.Empty(t, doc.KeyEpochs)
		require.Contains(t, out, "WARNING: this relay is UNAUTHENTICATED")
	}
}

// setMalformedRelayPeer models an operator editing a config file by hand.
func setMalformedRelayPeer(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("sync.relay.peer_id: invalid\n"), 0o600))
	svc, err := service.NewConfigService(path)
	require.NoError(t, err)
	app.configSvc = svc
}

func TestRelayCommands_MalformedConfiguredPeer_RefuseBeforeMutation(t *testing.T) {
	tests := []struct {
		name string
		make func() *cobra.Command
		args []string
	}{
		{"tick", newRelayTickCmd, nil},
		{"rotate", newRelayRotateKeyCmd, nil},
		{"clone export", newRelayCloneCmd, []string{"--export"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initCommandRelay(t, true)
			setMalformedRelayPeer(t)
			setDiagnosticConfig(t, "sync.relay.dir", dir)
			out, _, err := relayCommandOutput(tt.make(), tt.args...)
			require.ErrorContains(t, err, "sync.relay.peer_id")
			require.ErrorContains(t, err, "is malformed")
			require.NotContains(t, out, "published")
			require.NotContains(t, out, "installed")
			require.NotContains(t, out, "snapshot written")
		})
	}
}
