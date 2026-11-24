// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package relaygate_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

// gateRelay is an authenticated relay on one t.TempDir() shared directory with
// one in-process store per peer (FR-21 §11, MTIX-64.9.1). The shared directory
// is the only thing the peers have in common.
type gateRelay struct {
	dir   string
	key   []byte
	now   func() time.Time
	peers []tick.StackConfig
}

// newGateRelay initialises the relay and gives every peer the epoch key.
func newGateRelay(t *testing.T, ids ...string) gateRelay {
	t.Helper()
	now := testutil.FixedClock(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	g := gateRelay{dir: t.TempDir(), now: now}
	for i, id := range ids {
		g.peers = append(g.peers, tick.StackConfig{RelayDir: g.dir, KeysDir: t.TempDir(), PeerID: id,
			Store: newRelayGateStore(t, id, now), Now: now, Logger: slog.New(slog.DiscardHandler)})
		if i != 0 {
			continue
		}
		_, err := lifecycle.Init(lifecycle.InitRequest{
			RelayDir: g.dir, KeysDir: g.peers[0].KeysDir, CreatedBy: id, CreatedAt: now(),
			RelayID:       "01a0238b-d7d5-77cc-95c6-98a472ed7803",
			Projects:      []metadata.Project{{Prefix: "TEST", FirstEventHash: "aaaa"}},
			Authenticated: true, Rand: bytes.NewReader(bytes.Repeat([]byte{0x5a}, keyring.MinKeyBytes)),
		})
		require.NoError(t, err)
	}
	ring, err := keyring.Load(g.peers[0].KeysDir)
	require.NoError(t, err)
	epoch, key, err := ring.Current()
	require.NoError(t, err)
	g.key = key
	for _, p := range g.peers[1:] {
		require.NoError(t, keyring.Write(p.KeysDir, epoch, key))
	}
	return g
}

func (g gateRelay) sql(i int) *sqlite.Store { return g.peers[i].Store.(*sqlite.Store) }

// pass opens a fresh stack for peer i (a process restart each time) and runs
// one publish-then-ingest pass, failing on a returned error.
func (g gateRelay) pass(t *testing.T, i int) tick.Result {
	t.Helper()
	stack, err := tick.Open(g.peers[i])
	require.NoError(t, err)
	res, err := stack.Pass(t.Context())
	require.NoError(t, err)
	return res
}

// segDir is peer i's own segment directory on the shared medium.
func (g gateRelay) segDir(i int) string { return crashSegments(g.peers[i]) }

// segments lists peer i's segment files.
func (g gateRelay) segments(t *testing.T, i int) []segment.File {
	t.Helper()
	if _, err := os.Stat(g.segDir(i)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	files, foreign, err := segment.ListSegments(g.segDir(i))
	require.NoError(t, err)
	require.Empty(t, foreign)
	return files
}

// createNodes creates TEST-first..TEST-last on peer i.
func (g gateRelay) createNodes(t *testing.T, i, first, last int) {
	t.Helper()
	for seq := first; seq <= last; seq++ {
		createCrashNode(t, g.sql(i), seq)
	}
}

// nodeIDs returns the sorted node ids stored on peer i, "TEST-n" for n in 1..count.
func (g gateRelay) storedNodes(t *testing.T, i, count int) []string {
	t.Helper()
	var out []string
	for seq := 1; seq <= count; seq++ {
		id := fmt.Sprintf("TEST-%d", seq)
		n, err := g.sql(i).GetNode(t.Context(), id)
		if err != nil {
			require.ErrorIs(t, err, model.ErrNotFound)
			continue
		}
		require.Equal(t, id, n.Title, "node %s content", id)
		out = append(out, id)
	}
	return out
}
