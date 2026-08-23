// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package tick_test

import (
	"context"
	"crypto/rand"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

const (
	peerA = "0123456789abcdef"
	peerB = "fedcba9876543210"
)

var fixedTime = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

type relayRig struct {
	store    *sqlite.Store
	relayDir string
	keysDir  string
	made     int
}

func newRelay(t *testing.T, authenticated bool) *relayRig {
	t.Helper()
	root := t.TempDir()
	st, err := sqlite.New(filepath.Join(root, "store.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	relayDir := filepath.Join(root, "relay")
	keysDir := filepath.Join(root, "keys")
	require.NoError(t, os.MkdirAll(relayDir, 0o700))

	_, err = lifecycle.Init(lifecycle.InitRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		RelayID: "01a0238b-d7d5-77cc-95c6-98a472ed7803", CreatedAt: fixedTime, CreatedBy: peerA,
		Projects:      []metadata.Project{{Prefix: "PROJ", FirstEventHash: "aaaa"}},
		Authenticated: authenticated, Rand: rand.Reader,
	})
	require.NoError(t, err)
	return &relayRig{store: st, relayDir: relayDir, keysDir: keysDir}
}

func (r *relayRig) journal(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		r.made++
		id := "PROJ-" + string(rune('0'+r.made))
		require.NoError(t, r.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "PROJ", Depth: 0, Seq: r.made, Title: id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeStory, Creator: "t",
			ContentHash: model.ComputeContentHash(id, "", "", "", nil),
			CreatedAt:   fixedTime, UpdatedAt: fixedTime,
		}))
	}
}

func (r *relayRig) config() tick.StackConfig {
	return tick.StackConfig{
		Store: r.store, RelayDir: r.relayDir, KeysDir: r.keysDir,
		PeerID: peerA, Logger: slog.Default(),
	}
}

// TestOpen_BuildsBothHalvesFromTheMedium is the seam the CLI verb and
// the daemon phase both use. Two call sites building a relay by hand
// would drift, and the first thing to drift would be which mode the
// reader trusts.
func TestOpen_BuildsBothHalvesFromTheMedium(t *testing.T) {
	r := newRelay(t, true)
	r.journal(t, 2)

	stack, err := tick.Open(r.config())
	require.NoError(t, err)
	require.NotNil(t, stack.Publisher)
	require.NotNil(t, stack.Ingestor)
	require.True(t, stack.Relay.Authenticated)
	require.Equal(t, filepath.Join(r.relayDir, tick.PeersDirName), stack.PeersDir)

	res, err := stack.Pass(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, res.Published, "one pass publishes this peer's pending journal")

	// The peer's own directory was created for it.
	segDir := filepath.Join(r.relayDir, tick.PeersDirName, peerA, tick.SegmentsDirName)
	entries, err := os.ReadDir(segDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
}

// TestOpen_TakesTheModeFromTheRelayNotTheCaller is the fail-closed rule
// applied every pass rather than only at attach: a peer cannot read a
// relay in a weaker mode than the relay was created in, because the
// mode is not the caller's to assert.
func TestOpen_TakesTheModeFromTheRelayNotTheCaller(t *testing.T) {
	t.Run("an authenticated relay", func(t *testing.T) {
		r := newRelay(t, true)
		stack, err := tick.Open(r.config())
		require.NoError(t, err)
		require.True(t, stack.Relay.Authenticated)
	})
	t.Run("an unauthenticated relay needs no keys at all", func(t *testing.T) {
		r := newRelay(t, false)
		cfg := r.config()
		cfg.KeysDir = filepath.Join(t.TempDir(), "absent")

		stack, err := tick.Open(cfg)
		require.NoError(t, err)
		require.False(t, stack.Relay.Authenticated)
	})
}

// TestOpen_AuthenticatedRelayWithoutItsKeyIsRefused stops a peer that
// would attach, publish nothing readable, and report no reason.
func TestOpen_AuthenticatedRelayWithoutItsKeyIsRefused(t *testing.T) {
	r := newRelay(t, true)
	cfg := r.config()
	cfg.KeysDir = filepath.Join(t.TempDir(), "absent")

	_, err := tick.Open(cfg)
	require.ErrorIs(t, err, keyring.ErrKeyAbsent)
}

// TestOpen_Refusals covers the ways a relay cannot be opened.
func TestOpen_Refusals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tick.StackConfig)
	}{
		{"no store", func(c *tick.StackConfig) { c.Store = nil }},
		{"peer id off grammar", func(c *tick.StackConfig) { c.PeerID = "NOPE" }},
		{"not a relay directory", func(c *tick.StackConfig) { c.RelayDir = c.KeysDir + "-absent" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRelay(t, true)
			cfg := r.config()
			tt.mutate(&cfg)
			_, err := tick.Open(cfg)
			require.Error(t, err)
		})
	}
}

// TestStack_TwoPeersConverge drives the whole pass end to end: one peer
// publishes, the other ingests, through the same constructor.
func TestStack_TwoPeersConverge(t *testing.T) {
	ctx := context.Background()
	a := newRelay(t, true)
	a.journal(t, 3)

	stackA, err := tick.Open(a.config())
	require.NoError(t, err)
	_, err = stackA.Pass(ctx)
	require.NoError(t, err)

	// A second peer shares the relay directory and its key.
	bStore, err := sqlite.New(filepath.Join(t.TempDir(), "b.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = bStore.Close() })

	ringA, err := keyring.Load(a.keysDir)
	require.NoError(t, err)
	epoch, key, err := ringA.Current()
	require.NoError(t, err)
	keysB := filepath.Join(t.TempDir(), "keys-b")
	require.NoError(t, keyring.Write(keysB, epoch, key))

	stackB, err := tick.Open(tick.StackConfig{
		Store: bStore, RelayDir: a.relayDir, KeysDir: keysB,
		PeerID: peerB, Logger: slog.Default(),
	})
	require.NoError(t, err)

	res, err := stackB.Pass(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, res.Ingest.Applied, "the second peer applies what the first published")

	node, err := bStore.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Equal(t, "PROJ-1", node.Title)

	t.Run("a tick-only peer converges on its next pass and nothing else is required", func(t *testing.T) {
		a.journal(t, 1)
		_, err := stackA.Pass(ctx)
		require.NoError(t, err)

		res, err := stackB.Pass(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, res.Ingest.Applied)
	})
}

// TestOpen_AuthenticatedRelayWithNoRecordedEpoch is an internally
// inconsistent record: it claims authentication and names no key.
func TestOpen_AuthenticatedRelayWithNoRecordedEpoch(t *testing.T) {
	r := newRelay(t, true)
	require.NoError(t, os.WriteFile(filepath.Join(r.relayDir, metadata.FileName), []byte(
		`{"format_version":1,"relay_id":"r","created_at":"2026-08-23T12:00:00Z",`+
			`"created_by":"0123456789abcdef","projects":[{"project_prefix":"PROJ","first_event_hash":"a"}],`+
			`"authenticated":true,"key_epochs":[],"retired_peers":[]}`), 0o600))

	_, err := tick.Open(r.config())
	require.ErrorIs(t, err, metadata.ErrRelayCorrupt)
}

// TestOpen_UnwritablePeerDirectoryIsReported surfaces a read-only
// medium at open rather than at the first append.
func TestOpen_UnwritablePeerDirectoryIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	r := newRelay(t, true)
	require.NoError(t, os.Chmod(r.relayDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(r.relayDir, 0o700) })

	_, err := tick.Open(r.config())
	require.Error(t, err)
}
