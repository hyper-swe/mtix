// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests relay snapshot export/import, stamped positions, retirement clearing
// and bootstrap refusals without remote services or shared filesystem data.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestRelayCloneExport_PeerPositions_PersistsSnapshotAndPrivacyAdvice(t *testing.T) {
	dir := initCommandRelay(t, true)
	peerDir := filepath.Join(dir, tick.PeersDirName, diagnosticOther)
	require.NoError(t, os.MkdirAll(peerDir, 0o700))
	require.NoError(t, app.store.AdvanceRelayIngestCursor(t.Context(), diagnosticOther, sqlite.RelayIngestPosition{RS: 17, SegmentNo: 2, PubEpoch: 1}))
	out, diagnostic, err := relayCommandOutput(newRelayCloneCmd(), "--export")
	require.NoError(t, err)
	require.Empty(t, diagnostic)
	require.Contains(t, out, "snapshot written:")
	require.Contains(t, out, "NOTE: a snapshot is a full plaintext copy of this store.")
	names, err := bootstrap.SnapshotNames(dir)
	require.NoError(t, err)
	require.Len(t, names, 1)
	path := filepath.Join(dir, bootstrap.DirName, names[0])
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var snap bootstrap.Snapshot
	require.NoError(t, json.Unmarshal(body, &snap))
	require.Equal(t, diagnosticSelf, snap.ExportedBy)
	require.Equal(t, map[string]uint64{diagnosticOther: 17}, snap.Positions)
	require.False(t, snap.CreatedAt.IsZero())
	require.Len(t, snap.Export.Nodes, 1)
	require.Equal(t, "Relay command history", snap.Export.Nodes[0].Title)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestRelayCloneImport_NewestSnapshot_AdvancesPositionsAndClearsRetirement(t *testing.T) {
	dir := initCommandRelay(t, true)
	require.NoError(t, lifecycle.RetirePeer(dir, diagnosticSelf))
	// Export two snapshots at fixed, distinct timestamps; selection depends
	// on the newest valid name rather than directory enumeration or sleeps.
	for i, title := range []string{"Older snapshot task", "Newest snapshot task"} {
		require.NoError(t, runCreate(title, "", "", 3, "", "", "", "", ""))
		_, err := bootstrap.ExportSnapshot(t.Context(), bootstrap.ExportRequest{
			Store: app.store, RelayDir: dir, Project: "TEST", ExportedBy: diagnosticOther,
			CreatedAt: time.Date(2026, 1, 1+i, 12, 0, 0, 0, time.UTC),
			Positions: map[string]uint64{diagnosticOther: uint64(8 + i)},
		})
		require.NoError(t, err)
	}
	require.NoError(t, app.store.Close())
	replacement, err := sqlite.New(filepath.Join(t.TempDir(), "joining.db"), app.logger)
	require.NoError(t, err)
	app.store = replacement
	out, diagnostic, err := relayCommandOutput(newRelayCloneCmd())
	require.NoError(t, err)
	require.NotContains(t, diagnostic, "clear retirement")
	require.Contains(t, out, "imported 3 node(s); tailing from the stamped positions")
	node, err := app.store.GetNode(t.Context(), "TEST-3")
	require.NoError(t, err)
	require.Equal(t, "Newest snapshot task", node.Title)
	pos, err := app.store.RelayIngestCursor(t.Context(), diagnosticOther)
	require.NoError(t, err)
	require.Equal(t, uint64(9), pos.RS)
	doc, err := metadata.Read(dir)
	require.NoError(t, err)
	require.Empty(t, doc.RetiredPeers)
}

func TestRelayCloneImport_InvalidSnapshotOrFlags_RefusesWithoutSuccess(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no snapshot", nil, "no bootstrap snapshot"},
		{"bad workflow choice", []string{"--prefer", "invalid"}, "--prefer"},
		{"blocked snapshot directory", nil, ""},
		{"malformed snapshot", nil, "snapshot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initCommandRelay(t, true)
			root := filepath.Join(dir, bootstrap.DirName)
			switch tt.name {
			case "blocked snapshot directory":
				require.NoError(t, os.WriteFile(root, []byte("owned obstruction"), 0o600))
			case "malformed snapshot":
				require.NoError(t, os.Mkdir(root, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(root, "snap-20260101120000-"+diagnosticOther+bootstrap.FileExt), []byte("invalid JSON"), 0o600))
			}
			before, err := app.store.JournalTail(t.Context())
			require.NoError(t, err)
			out, _, err := relayCommandOutput(newRelayCloneCmd(), tt.args...)
			require.Error(t, err)
			if tt.want != "" {
				require.ErrorContains(t, err, tt.want)
			}
			require.NotContains(t, out, "imported")
			after, err := app.store.JournalTail(t.Context())
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestRelayCloneExport_NaturalFailures_RefuseWithoutSnapshot(t *testing.T) {
	for _, scenario := range []string{"blocked peers", "closed store", "cancelled context", "blocked bootstrap"} {
		t.Run(scenario, func(t *testing.T) {
			dir := initCommandRelay(t, true)
			cmd := newRelayCloneCmd()
			switch scenario {
			case "blocked peers":
				require.NoError(t, os.WriteFile(filepath.Join(dir, tick.PeersDirName), []byte("owned obstruction"), 0o600))
			case "closed store":
				require.NoError(t, app.store.Close())
			case "cancelled context":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				cmd.SetContext(ctx)
			case "blocked bootstrap":
				require.NoError(t, os.WriteFile(filepath.Join(dir, bootstrap.DirName), []byte("owned obstruction"), 0o600))
			}
			out, _, err := relayCommandOutput(cmd, "--export")
			require.Error(t, err)
			require.NotContains(t, out, "snapshot written")
		})
	}
}

func TestRelayPeerPositions_EmptyAndClosedStore_ReflectDurableState(t *testing.T) {
	dir := initCommandRelay(t, true)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, tick.PeersDirName), 0o700))
	positions, err := relayPeerPositions(t.Context(), dir)
	require.NoError(t, err)
	require.Empty(t, positions)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, tick.PeersDirName, diagnosticOther), 0o700))
	require.NoError(t, app.store.Close())
	positions, err = relayPeerPositions(t.Context(), dir)
	require.Error(t, err)
	require.Nil(t, positions)
}
