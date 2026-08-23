// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
)

func newRelayConfig(t *testing.T) *service.ConfigService {
	t.Helper()
	cs, err := service.NewConfigService(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	return cs
}

// TestConfig_RelayKeysAreAllowed adds the FR-21 transport to the FR-11.2
// allowlist. A key absent from it is rejected outright, so the relay is
// unconfigurable until they are named here.
func TestConfig_RelayKeysAreAllowed(t *testing.T) {
	cs := newRelayConfig(t)
	tests := []struct {
		key   string
		value string
	}{
		{"sync.relay.dir", "/mnt/bridge/team-relay"},
		{"sync.relay.peer_id", "0123456789abcdef-desk"},
		{"sync.relay.poll_interval", "5s"},
		{"sync.relay.retention_days", "7"},
		{"sync.relay.silent_peer_days", "14"},
		{"sync.relay.max_segment_bytes", "4194304"},
		{"sync.relay.require_auth", "true"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			_, err := cs.Set(tt.key, tt.value)
			require.NoError(t, err)
			got, err := cs.Get(tt.key)
			require.NoError(t, err)
			require.Equal(t, tt.value, got)
		})
	}
}

// TestConfig_RelayDefaults pins the shipped defaults. Two matter beyond
// convenience: poll_interval is its own setting rather than the daemon's
// tick (D-R12 — the daemon's cadence and the medium's are unrelated
// concerns), and require_auth defaults ON, so an unauthenticated relay
// is always a deliberate opt-out.
func TestConfig_RelayDefaults(t *testing.T) {
	cs := newRelayConfig(t)
	tests := []struct{ key, want string }{
		{"sync.relay.dir", ""},
		{"sync.relay.peer_id", ""},
		{"sync.relay.poll_interval", "5s"},
		{"sync.relay.retention_days", "7"},
		{"sync.relay.silent_peer_days", "14"},
		{"sync.relay.max_segment_bytes", "4194304"},
		{"sync.relay.require_auth", "true"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, err := cs.Get(tt.key)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestConfig_PollIntervalZeroIsLegal is D-R12's tick-only peer. A peer
// that declares no polling is a valid deployment — a turn-driven agent
// seat, a cron-locked appliance, a sneakernet courier — not a
// misconfiguration, so setting it must simply work.
func TestConfig_PollIntervalZeroIsLegal(t *testing.T) {
	cs := newRelayConfig(t)
	_, err := cs.Set("sync.relay.poll_interval", "0")
	require.NoError(t, err)

	got, gErr := cs.Get("sync.relay.poll_interval")
	require.NoError(t, gErr)
	require.Equal(t, "0", got)
}

// TestConfig_PeerIDIsValidatedAtSetTime is FR-21 §5.2/§7.1's stable
// identity. It is validated where an operator finds out — at the moment
// they set it — rather than at the next tick, when the message would
// arrive detached from the action that caused it.
//
// An empty value is legal and means "derive from this machine", which
// is the default for a peer whose workspace is not rebuilt elsewhere.
func TestConfig_PeerIDIsValidatedAtSetTime(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"bare machine hash", "0123456789abcdef", false},
		{"labelled", "0123456789abcdef-desk", false},
		{"label at full length", "0123456789abcdef-" + strings.Repeat("a", 32), false},
		{"empty means derive from the machine", "", false},
		{"too short", "0123456789abcde", true},
		{"uppercase hex", "0123456789ABCDEF", true},
		{"not hex", "0123456789abcdeg", true},
		{"label too long", "0123456789abcdef-" + strings.Repeat("a", 33), true},
		{"path separator", "0123456789abcdef/x", true},
		{"a whole path", "../../etc/passwd", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := newRelayConfig(t)
			_, err := cs.Set("sync.relay.peer_id", tt.value)
			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				return
			}
			require.NoError(t, err)
			got, err := cs.Get("sync.relay.peer_id")
			require.NoError(t, err)
			require.Equal(t, tt.value, got)
		})
	}
}

// TestConfig_RelayKeysSurviveAFileRoundTrip pins the property that makes
// them usable at all: a key that does not come back after a reload is a
// key that silently does nothing. The config file nests one level and
// joins the rest, so the relay's three-part names are worth checking
// rather than assuming.
func TestConfig_RelayKeysSurviveAFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)

	want := map[string]string{
		"sync.relay.dir":               "/mnt/shared/team-relay",
		"sync.relay.peer_id":           "0123456789abcdef-desk",
		"sync.relay.poll_interval":     "0",
		"sync.relay.retention_days":    "14",
		"sync.relay.silent_peer_days":  "30",
		"sync.relay.max_segment_bytes": "1048576",
		"sync.relay.require_auth":      "false",
	}
	for key, value := range want {
		_, err := cs.Set(key, value)
		require.NoError(t, err)
	}

	reloaded, err := service.NewConfigService(path)
	require.NoError(t, err)
	for key, value := range want {
		got, err := reloaded.Get(key)
		require.NoError(t, err)
		require.Equalf(t, value, got, "%s did not survive the round trip", key)
	}
}

// TestConfig_UnknownRelayKeyIsStillRejected keeps the allowlist an
// allowlist: adding a family does not open a namespace.
func TestConfig_UnknownRelayKeyIsStillRejected(t *testing.T) {
	cs := newRelayConfig(t)
	for _, key := range []string{
		"sync.relay",
		"sync.relay.peer_id_unknown",
		"sync.relay.dir.extra",
		"relay.dir",
	} {
		_, err := cs.Set(key, "x")
		require.ErrorIs(t, err, model.ErrInvalidConfigKey, "key %q", key)
	}
}

// TestConfig_RelayKeysAppearInTheRefusalMessage keeps `mtix config`
// honest — a key nobody can discover is a key nobody sets correctly, so
// the list an unknown key prints must name the relay family.
func TestConfig_RelayKeysAppearInTheRefusalMessage(t *testing.T) {
	cs := newRelayConfig(t)
	_, err := cs.Set("sync.relay.nonsense", "x")
	require.Error(t, err)
	for _, key := range []string{
		"sync.relay.dir", "sync.relay.peer_id", "sync.relay.poll_interval",
		"sync.relay.retention_days", "sync.relay.silent_peer_days",
		"sync.relay.max_segment_bytes", "sync.relay.require_auth",
	} {
		require.Contains(t, err.Error(), key)
	}
}
