// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/stretchr/testify/require"
)

// TestRetirePeer_AppendsToTheRoster covers `relay retire-peer`.
func TestRetirePeer_AppendsToTheRoster(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	require.NoError(t, lifecycle.RetirePeer(relayDir, peerB))

	doc, err := metadata.Read(relayDir)
	require.NoError(t, err)
	require.Equal(t, []string{peerB}, doc.RetiredPeers)

	t.Run("retiring twice is idempotent, not an error", func(t *testing.T) {
		require.NoError(t, lifecycle.RetirePeer(relayDir, peerB))
		doc, err := metadata.Read(relayDir)
		require.NoError(t, err)
		require.Equal(t, []string{peerB}, doc.RetiredPeers, "the roster is a set")
	})

	t.Run("and a second peer joins the roster", func(t *testing.T) {
		require.NoError(t, lifecycle.RetirePeer(relayDir, "aaaaaaaaaaaaaaaa"))
		doc, err := metadata.Read(relayDir)
		require.NoError(t, err)
		require.ElementsMatch(t, []string{peerB, "aaaaaaaaaaaaaaaa"}, doc.RetiredPeers)
	})
}

// TestRetirePeer_Refusals keeps a malformed retirement, or one aimed at
// a relay that cannot be read, out of the roster.
func TestRetirePeer_Refusals(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	t.Run("a malformed peer id", func(t *testing.T) {
		require.Error(t, lifecycle.RetirePeer(relayDir, "NOT A PEER"))
	})
	t.Run("an unreadable relay", func(t *testing.T) {
		require.Error(t, lifecycle.RetirePeer(t.TempDir(), peerB))
	})
}

// TestRejoinPeer_ClearsRetirement is the FR-21 §6.7 return path: a
// retired peer that comes back re-enters through bootstrap, and that
// clears its retirement so it rejoins the prune quorum.
//
// Without this, a peer could be retired once and then hold nothing —
// its acks would be ignored forever while it published real work, and
// writers would prune history it still needed.
func TestRejoinPeer_ClearsRetirement(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)
	require.NoError(t, lifecycle.RetirePeer(relayDir, peerB))

	require.NoError(t, lifecycle.RejoinPeer(relayDir, peerB))

	doc, err := metadata.Read(relayDir)
	require.NoError(t, err)
	require.Empty(t, doc.RetiredPeers)

	t.Run("rejoining a peer that was never retired is a no-op", func(t *testing.T) {
		require.NoError(t, lifecycle.RejoinPeer(relayDir, peerB))
	})

	t.Run("a malformed peer id is refused", func(t *testing.T) {
		require.Error(t, lifecycle.RejoinPeer(relayDir, "NOT A PEER"))
	})

	t.Run("an unreadable relay is reported", func(t *testing.T) {
		require.Error(t, lifecycle.RejoinPeer(t.TempDir(), peerB))
	})

	t.Run("and only that peer leaves the roster", func(t *testing.T) {
		require.NoError(t, lifecycle.RetirePeer(relayDir, peerB))
		require.NoError(t, lifecycle.RetirePeer(relayDir, "aaaaaaaaaaaaaaaa"))
		require.NoError(t, lifecycle.RejoinPeer(relayDir, peerB))

		doc, err := metadata.Read(relayDir)
		require.NoError(t, err)
		require.Equal(t, []string{"aaaaaaaaaaaaaaaa"}, doc.RetiredPeers)
	})
}

// TestRetiredPeers_IsTheQuorumView gives the pruner its roster in the
// shape it consumes.
func TestRetiredPeers_IsTheQuorumView(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)
	require.NoError(t, lifecycle.RetirePeer(relayDir, peerB))

	doc, err := metadata.Read(relayDir)
	require.NoError(t, err)
	retired := lifecycle.RetiredSet(doc)
	require.True(t, retired[peerB])
	require.False(t, retired[peerA])

	t.Run("a nil document has an empty roster", func(t *testing.T) {
		require.Empty(t, lifecycle.RetiredSet(nil))
	})
}
