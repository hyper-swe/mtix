// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build faultinject && !windows

package faultinject

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

type relayPeer struct {
	project string
	env     []string
}

func newRelayPeer(t *testing.T, id string) relayPeer {
	t.Helper()
	p := relayPeer{project: t.TempDir(), env: []string{
		"HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + t.TempDir(),
	}}
	p.run(t, "init", "--prefix", "FI")
	seedRelayMachineHash(t, p.project, id)
	p.run(t, "config", "set", "sync.relay.peer_id", id)
	p.run(t, "config", "set", "sync.relay.require_auth", "false")
	return p
}

// Seed the store's usual identity cache so the fixture needs no access to host
// network interfaces even when the test runs in a sandbox.
func seedRelayMachineHash(t *testing.T, project, id string) {
	t.Helper()
	st, err := sqlite.New(filepath.Join(project, ".mtix", "data", "mtix.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	require.NoError(t, st.WithTx(t.Context(), func(tx *sql.Tx) error {
		// Give each test peer a stable author machine hash before its first event.
		_, err := tx.ExecContext(t.Context(), `UPDATE meta SET value = ? WHERE key = 'meta.sync.machine_hash'`, id)
		return err
	}))
}

func (p relayPeer) run(t *testing.T, args ...string) []byte {
	t.Helper()
	out, err := runMtix(p.project, p.env, args...)
	require.NoError(t, err, "%v\n%s", args, out)
	return out
}

// TestRelayReader_ExplainedTornSeal_Converges exercises the FR-21 §5.5
// recovery prerequisite for the Unix SIGKILL scenarios in MTIX-64.9.3.
func TestRelayReader_ExplainedTornSeal_Converges(t *testing.T) {
	const publisherID = "0123456789abcdef"
	writer := newRelayPeer(t, publisherID)
	reader := newRelayPeer(t, "fedcba9876543210")
	dir := t.TempDir()
	writer.run(t, "create", "before interruption")
	writer.run(t, "sync", "relay", "init", "--no-auth", dir)
	writer.run(t, "config", "set", "sync.relay.dir", dir)
	reader.run(t, "config", "set", "sync.relay.dir", dir)
	writer.run(t, "sync", "relay", "tick")

	// Leave the first byte of the next record as a mid-append crash would.
	segments := filepath.Join(dir, "peers", publisherID, "segments")
	f, err := os.OpenFile(filepath.Join(segments, segment.FileName(1)), os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, writeErr := f.Write([]byte("M"))
	closeErr := f.Close()
	require.NoError(t, writeErr)
	require.NoError(t, closeErr)

	writer.run(t, "create", "after restart")
	writer.run(t, "sync", "relay", "tick")
	assertExplainedSeal(t, segments, publisherID)
	pass := reader.run(t, "sync", "relay", "tick")
	var status struct {
		Peers []struct {
			ID      string `json:"peer_id"`
			Segment uint64 `json:"ingested_segment"`
			RS      uint64 `json:"ingested_rs"`
		} `json:"peers"`
	}
	// runMtix combines stdout with trailing stderr diagnostics.
	raw := reader.run(t, "sync", "relay", "status", "--json")
	require.NoError(t, json.NewDecoder(bytes.NewReader(raw)).Decode(&status), "%s", raw)
	for _, peer := range status.Peers {
		if peer.ID == publisherID {
			require.Equal(t, uint64(2), peer.Segment, "reader position %+v after tick:\n%s", peer, pass)
			require.Contains(t, string(reader.run(t, "list")), "after restart")
			return
		}
	}
	t.Fatalf("publisher absent from reader status: %+v", status)
}

func assertExplainedSeal(t *testing.T, dir, peerID string) {
	t.Helper()
	sealed, err := segment.ScanFile(filepath.Join(dir, segment.FileName(1)), segment.ScanOptions{
		Sealed: true, TolerateTornTail: true, ExpectPeerID: peerID,
	})
	require.NoError(t, err)
	next, err := segment.ScanFile(filepath.Join(dir, segment.FileName(2)), segment.ScanOptions{
		Sealed: true, ExpectPeerID: peerID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, next.Records)
	require.True(t, segment.ExplainsTornSeal(sealed, next.Header))
}
