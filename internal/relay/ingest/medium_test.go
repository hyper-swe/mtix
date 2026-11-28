// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

const otherPeer = "aaaaaaaaaaaaaaaa"

// publishOther writes a one-event segment for a second, healthy peer.
func publishOther(t *testing.T, r *peerRig) {
	t.Helper()
	dir := filepath.Join(r.peersDir, otherPeer, "segments")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	e := event(t, model.OpCreateNode, "PROJ-9", 1, &model.CreateNodePayload{Title: "other"})
	e.AuthorMachineHash = otherPeer
	h := segment.Header{FormatVersion: segment.FormatVersion, Flags: segment.FlagAuthenticated,
		PeerID: otherPeer, SegmentNo: 1, FirstRS: 1, KeyEpoch: 1, PubEpoch: 1}
	raw, err := h.MarshalBinary()
	require.NoError(t, err)
	raw, err = segment.AppendRecord(raw, h, 1, marshalEvents(t, e)[0], fleetKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, segment.FileName(1)), raw, 0o600))
}

func poll(t *testing.T, in *ingest.Ingestor) ingest.Stats {
	t.Helper()
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err, "a medium fault on one peer must not fail the poll")
	return stats
}

func requireMediumStall(t *testing.T, st ingest.Stall, class string) {
	t.Helper()
	require.Equal(t, "RELAY_MEDIUM_UNREADABLE", st.Code)
	require.Contains(t, st.Reason, "peers/"+remotePeer+"/segments/")
	require.Contains(t, st.Reason, class)
	require.NotContains(t, st.Reason, os.TempDir(), "no absolute host path in the message")
}

func TestRelayIngest_PermissionDeniedSegmentStallsOnlyThatPeerAndClears(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	r := newPeerRig(t)
	r.publish(t, fleetKey, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"}))...)
	publishOther(t, r)
	path := filepath.Join(r.segDir, segment.FileName(1))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats := poll(t, in)
	require.Len(t, stats.Stalls, 1, "only the unreadable peer stalls")
	require.Equal(t, remotePeer, stats.Stalls[0].PeerID)
	requireMediumStall(t, stats.Stalls[0], "permission denied")
	require.Equal(t, 1, stats.Applied, "the healthy peer keeps ingesting")
	require.Zero(t, r.cursor(t).RS)

	require.NoError(t, os.Chmod(path, 0o600))
	stats = poll(t, in)
	require.Empty(t, stats.Stalls, "the code clears on the next successful read")
	require.Equal(t, 1, stats.Applied)
	require.Equal(t, uint64(1), r.cursor(t).RS)
}

func TestRelayIngest_UnreadableSegmentsDirectoryStallsWithCode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	r := newPeerRig(t)
	r.publish(t, fleetKey, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"}))...)
	require.NoError(t, os.Chmod(r.segDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(r.segDir, 0o700) })

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats := poll(t, in)
	require.Len(t, stats.Stalls, 1)
	require.Equal(t, "RELAY_MEDIUM_UNREADABLE", stats.Stalls[0].Code)
	require.True(t, strings.HasSuffix(stats.Stalls[0].Reason, "peers/"+remotePeer+"/segments: permission denied"), stats.Stalls[0].Reason)
}

func TestRelayIngest_InjectedReadErrorStallsBanksPositionAndClears(t *testing.T) {
	r := newPeerRig(t)
	r.publish(t, fleetKey, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "a"}))...)
	r.publish(t, fleetKey, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "b"}))...)
	publishOther(t, r)
	bad := filepath.Join(r.segDir, segment.FileName(2))

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	ingest.InjectSegmentRead(in, func(p string) ([]byte, error) {
		if p == bad {
			return nil, &fs.PathError{Op: "read", Path: p, Err: syscall.EIO}
		}
		return os.ReadFile(p)
	})
	stats := poll(t, in)
	require.Len(t, stats.Stalls, 1)
	require.Equal(t, remotePeer, stats.Stalls[0].PeerID)
	require.Equal(t, uint64(2), stats.Stalls[0].Segment)
	requireMediumStall(t, stats.Stalls[0], "input/output error")
	require.Equal(t, 2, stats.Applied, "segment 1 of the stalled peer and the healthy peer applied")
	require.Equal(t, uint64(1), r.cursor(t).RS, "the stall banks the applied position")

	ingest.InjectSegmentRead(in, os.ReadFile)
	stats = poll(t, in)
	require.Empty(t, stats.Stalls)
	require.Equal(t, 1, stats.Applied)
	require.Equal(t, uint64(2), r.cursor(t).RS)
}

func TestRelayIngest_InjectedNotExistKeepsItsMeaning(t *testing.T) {
	r := newPeerRig(t)
	r.publish(t, fleetKey, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "a"}))...)
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	ingest.InjectSegmentRead(in, func(p string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	})
	stats := poll(t, in)
	require.Len(t, stats.Stalls, 1)
	require.Empty(t, stats.Stalls[0].Code, "not-exist is not a medium fault")
}

func TestProbeMedium_NamesUnreadablePeersWithoutApplying(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	r := newPeerRig(t)
	r.publish(t, fleetKey, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"}))...)
	publishOther(t, r)
	path := filepath.Join(r.segDir, segment.FileName(1))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	cursor := func(string) (ingest.Position, error) { return ingest.Position{}, nil }

	stalls, err := ingest.ProbeMedium(r.peersDir, selfPeer, cursor)
	require.NoError(t, err)
	require.Len(t, stalls, 1)
	require.Equal(t, remotePeer, stalls[0].PeerID)
	requireMediumStall(t, stalls[0], "permission denied")
	require.Zero(t, r.cursor(t).RS, "a probe moves no cursor")

	require.NoError(t, os.Chmod(path, 0o600))
	stalls, err = ingest.ProbeMedium(r.peersDir, selfPeer, cursor)
	require.NoError(t, err)
	require.Empty(t, stalls)
}
