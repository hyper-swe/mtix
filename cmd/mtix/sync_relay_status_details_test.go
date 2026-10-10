// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests relay status collection, peer positions and activity, filesystem
// timestamps, and human and JSON reports using isolated relay fixtures.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// diagnosticStatusFixture seeds positions through the public store API, independent of status collection.
func diagnosticStatusFixture(t *testing.T) relayStatusReport {
	t.Helper()
	dir, doc := initDiagnosticRelay(t, true)
	diagnosticSegment(t, dir, diagnosticRetired, 1, diagnosticOld())
	diagnosticSegment(t, dir, diagnosticOther, 1, diagnosticOld())
	diagnosticSegment(t, dir, diagnosticSelf, 1, diagnosticCurrent())
	require.NoError(t, doc.AppendKeyEpoch(2, map[string]uint64{diagnosticSelf: 12}))
	doc.RetiredPeers = []string{diagnosticRetired}
	require.NoError(t, metadata.Rewrite(dir, doc))
	require.NoError(t, app.store.ResetRelayPublisher(t.Context(), 0, 12))
	require.NoError(t, app.store.AdvanceRelayIngestCursor(t.Context(), diagnosticOther,
		sqlite.RelayIngestPosition{SegmentNo: 3, RS: 29, PubEpoch: 4}))
	require.NoError(t, app.store.AdvanceRelayIngestCursor(t.Context(), diagnosticRetired,
		sqlite.RelayIngestPosition{SegmentNo: 7, RS: 51, PubEpoch: 2}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "peers", "notes.txt"), nil, 0o600))
	const snapshot = "snap-20260101120000-0123456789abcdef.mrsnap"
	diagnosticSnapshot(t, dir, snapshot, diagnosticCurrent())
	return relayStatusReport{
		Dir: dir, RelayID: "diagnostic-relay", Authenticated: true, KeyEpoch: 2, Self: diagnosticSelf,
		PublishedRS: 11, PublisherEpoch: 2,
		Peers: []relayPeerStatus{
			{PeerID: diagnosticSelf, Self: true},
			{PeerID: diagnosticOther, SegmentNo: 3, RS: 29, PubEpoch: 4, Silent: true},
			{PeerID: diagnosticRetired, SegmentNo: 7, RS: 51, PubEpoch: 2, Retired: true},
		},
		Snapshots: []string{snapshot}, ForeignEntries: []string{"notes.txt"},
	}
}

func TestCollectRelayStatus_PeerPositions_ReturnsSortedReport(t *testing.T) {
	want := diagnosticStatusFixture(t)
	got, err := collectRelayStatus(t.Context())
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestCollectRelayStatus_UnauthenticatedFreshStore_ReturnsZeroPositions(t *testing.T) {
	dir, _ := initDiagnosticRelay(t, false)
	got, err := collectRelayStatus(t.Context())
	require.NoError(t, err)
	require.Equal(t, relayStatusReport{
		Dir: dir, RelayID: "diagnostic-relay", Self: diagnosticSelf, PublisherEpoch: 1,
		Snapshots: []string{},
	}, got)
}

func TestCollectRelayStatus_UnreadableOptionalState_KeepsRoster(t *testing.T) {
	for _, state := range []string{"closed store", "bootstrap file"} {
		t.Run(state, func(t *testing.T) {
			dir, _ := initDiagnosticRelay(t, true)
			diagnosticSegment(t, dir, diagnosticSelf, 1, diagnosticCurrent())
			if state == "closed store" {
				require.NoError(t, app.store.Close())
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(dir, bootstrap.DirName), nil, 0o600))
			}
			got, err := collectRelayStatus(t.Context())
			require.NoError(t, err)
			require.Equal(t, []relayPeerStatus{{PeerID: diagnosticSelf, Self: true}}, got.Peers)
			require.Zero(t, got.PublishedRS)
			if state == "closed store" {
				require.Zero(t, got.PublisherEpoch)
			} else {
				require.Equal(t, uint16(1), got.PublisherEpoch)
			}
			if state == "bootstrap file" {
				require.Nil(t, got.Snapshots)
			}
		})
	}
}

func TestCollectRelayStatus_InvalidRequiredState_ReturnsError(t *testing.T) {
	tests := []struct{ name, want string }{
		{"unconfigured", "no relay configured; run `mtix config set sync.relay.dir <path>`"},
		{"missing record", "RELAY_META_ABSENT"},
		{"malformed record", "RELAY_META_CORRUPT"},
		{"malformed peer", "sync.relay.peer_id"},
		{"peers symlink", "RELAY_SYMLINK"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, _ := initDiagnosticRelay(t, true)
			switch tt.name {
			case "unconfigured":
				setDiagnosticConfig(t, "sync.relay.dir", "")
			case "missing record":
				require.NoError(t, os.Remove(filepath.Join(dir, metadata.FileName)))
			case "malformed record":
				require.NoError(t, os.WriteFile(filepath.Join(dir, metadata.FileName), []byte("{"), 0o644))
			case "malformed peer":
				path := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte("sync:\n  relay.dir: "+dir+"\n  relay.peer_id: malformed\n"), 0o600))
				config, err := service.NewConfigService(path)
				require.NoError(t, err)
				app.configSvc = config
			case "peers symlink":
				path := filepath.Join(dir, "peers")
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink(t.TempDir(), path))
			}
			got, err := collectRelayStatus(t.Context())
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
			require.Equal(t, relayStatusReport{}, got)
		})
	}
}

func TestRelayLastSeen_SegmentTimes_UsesNewestMtime(t *testing.T) {
	dir := t.TempDir()
	old := diagnosticOld()
	current := diagnosticCurrent()
	// Segment number is ordering of records, not evidence of wall-clock activity.
	diagnosticSegment(t, dir, diagnosticSelf, 1, current)
	diagnosticSegment(t, dir, diagnosticSelf, 2, old)
	empty := filepath.Join(dir, "peers", diagnosticOther, "segments")
	require.NoError(t, os.MkdirAll(empty, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(empty, "notes.txt"), nil, 0o600))
	got := relayLastSeen(dir, []string{diagnosticSelf, diagnosticOther, diagnosticRetired})
	require.Len(t, got, 1)
	require.True(t, current.Equal(got[diagnosticSelf]), "newest mtime must win, regardless of timezone representation")
}

func TestRelayStat_MissingAndSymlink_ReturnsLstatTimestamp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(target, nil, 0o600))
	require.NoError(t, os.Chtimes(target, diagnosticOld(), diagnosticOld()))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(target, link))
	info, err := os.Lstat(link)
	require.NoError(t, err)
	got, err := relayStat(link)
	require.NoError(t, err)
	require.Equal(t, info.ModTime(), got, "link timestamp must not come from its target")
	require.NotEqual(t, diagnosticOld(), got)
	got, err = relayStat(filepath.Join(dir, "absent"))
	require.Error(t, err)
	require.True(t, got.IsZero())
}

func TestPrintRelayStatus_ModesAndPeerMarks_RendersOperatorActions(t *testing.T) {
	for _, authenticated := range []bool{true, false} {
		t.Run(map[bool]string{true: "authenticated", false: "unauthenticated"}[authenticated], func(t *testing.T) {
			rep := relayStatusReport{
				Dir: "/fixture/relay", RelayID: "r1", Authenticated: authenticated, KeyEpoch: 3,
				Self: diagnosticSelf, PublishedRS: 23, PublisherEpoch: 4,
				Peers: []relayPeerStatus{
					{PeerID: diagnosticSelf, SegmentNo: 1, RS: 2, PubEpoch: 3, Self: true, Retired: true, Silent: true},
					{PeerID: diagnosticRetired, SegmentNo: 4, RS: 5, PubEpoch: 6, Retired: true, Silent: true},
					{PeerID: diagnosticOther, SegmentNo: 7, RS: 8, PubEpoch: 9, Silent: true},
					{PeerID: "3123456789abcdef-active", SegmentNo: 10, RS: 11, PubEpoch: 12},
				}, Snapshots: []string{"snap-fixture.mrsnap"}, ForeignEntries: []string{"notes.txt"},
			}
			var out bytes.Buffer
			cmd := &cobra.Command{Use: "status"}
			cmd.SetOut(&out)
			printRelayStatus(cmd, rep)
			mode := "UNAUTHENTICATED"
			if authenticated {
				mode = "authenticated, key epoch 3"
			}
			require.Equal(t, "relay r1 at /fixture/relay ("+mode+")\n"+
				"this peer "+diagnosticSelf+": published through rs 23, publisher epoch 4\n"+
				"peers:\n"+
				"  "+diagnosticSelf+"  ingested segment 1 rs 2 (epoch 3) (this peer)\n"+
				"  "+diagnosticRetired+"  ingested segment 4 rs 5 (epoch 6) RETIRED\n"+
				"  "+diagnosticOther+"  ingested segment 7 rs 8 (epoch 9) SILENT — consider `mtix sync relay retire-peer "+diagnosticOther+"`\n"+
				"  3123456789abcdef-active  ingested segment 10 rs 11 (epoch 12)\n"+
				"bootstrap snapshot present: snap-fixture.mrsnap\nforeign entry ignored: notes.txt\n", out.String())
		})
	}
}

func TestNewRelayStatusCmd_OutputModes_ExposeCollectedValues(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(map[bool]string{false: "human", true: "json"}[asJSON], func(t *testing.T) {
			want := diagnosticStatusFixture(t)
			var out bytes.Buffer
			cmd := newRelayStatusCmd()
			cmd.SetOut(&out)
			cmd.SetContext(t.Context())
			args := []string{}
			if asJSON {
				args = append(args, "--json")
			}
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			if asJSON {
				var got relayStatusReport
				require.NoError(t, json.Unmarshal(out.Bytes(), &got))
				require.Equal(t, want, got)
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(out.Bytes(), &fields))
				require.Len(t, fields, 10)
				require.Contains(t, fields, "published_rs")
				require.Contains(t, fields, "foreign_entries")
			} else {
				require.Contains(t, out.String(), "authenticated, key epoch 2")
				require.Contains(t, out.String(), "published through rs 11, publisher epoch 2")
				require.Contains(t, out.String(), diagnosticOther+"  ingested segment 3 rs 29 (epoch 4) SILENT")
				require.Contains(t, out.String(), diagnosticRetired+"  ingested segment 7 rs 51 (epoch 2) RETIRED")
				require.Contains(t, out.String(), "bootstrap snapshot present: "+want.Snapshots[0])
				require.Contains(t, out.String(), "foreign entry ignored: notes.txt")
			}
		})
	}
}

func TestRelayPeerDirs_ValidAndForeignEntries_AreSeparated(t *testing.T) {
	dir := t.TempDir()
	diagnosticSegment(t, dir, diagnosticOther, 1, diagnosticCurrent())
	diagnosticSegment(t, dir, diagnosticSelf, 1, diagnosticCurrent())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "peers", "notes.txt"), nil, 0o600))
	peers, foreign, err := relayPeerDirs(dir)
	require.NoError(t, err)
	require.Equal(t, []string{diagnosticSelf, diagnosticOther}, peers)
	require.Equal(t, []string{"notes.txt"}, foreign)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "peers", diagnosticRetired), nil, 0o600))
	_, _, err = relayPeerDirs(dir)
	require.ErrorIs(t, err, segment.ErrForeignEntry)
}
