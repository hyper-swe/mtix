// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// Tests that every place the relay reader touches segment bytes or the
// segments directory classifies a read fault as RELAY_MEDIUM_UNREADABLE, never
// RELAY_SEGMENT_CORRUPT, and that the status probe agrees with a poll
// (FR-21 section 9, MTIX-290).

func segFile(r *peerRig, no uint64) string { return filepath.Join(r.segDir, segment.FileName(no)) }

// tornWithSuccessor publishes a sealed segment 1 that ends in a torn tail and
// a successor segment 2 that explains it.
func tornWithSuccessor(t *testing.T, r *peerRig) {
	t.Helper()
	records := tornPrefix(t, r)
	r.rs = 2
	r.publish(t, fleetKey, records[1:]...)
}

// denyAccess makes path unreadable until the test ends.
func denyAccess(t *testing.T, path string, restore os.FileMode) {
	t.Helper()
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, restore) })
}

func TestRelayIngest_TornSealSuccessorReadFaultStallsMediumBanksAndClears(t *testing.T) {
	r := newPeerRig(t)
	tornWithSuccessor(t, r)
	bad := segFile(r, 2)
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
	st := stats.Stalls[0]
	require.Equal(t, remotePeer, st.PeerID)
	require.Equal(t, uint64(1), st.Segment, "the stall is on the torn sealed segment")
	require.Equal(t, "RELAY_MEDIUM_UNREADABLE", st.Code, "never RELAY_SEGMENT_CORRUPT")
	require.Contains(t, st.Reason, "peers/"+remotePeer+"/segments/"+segment.FileName(2), "names the unreadable successor")
	require.Contains(t, st.Reason, "input/output error")
	require.NotContains(t, st.Reason, os.TempDir(), "no absolute host path in the message")
	require.Equal(t, 2, stats.Applied, "the whole records before the torn tail applied")
	require.Zero(t, r.cursor(t).SegmentNo, "as for any verdict stall inside a segment, the cursor stays before it")
	require.Equal(t, 2, r.journalCount(t), "the whole records are in the journal: nothing applied is lost")

	ingest.InjectSegmentRead(in, os.ReadFile)
	stats = poll(t, in)
	require.Empty(t, stats.Stalls, "the stall clears once the successor reads")
	require.Equal(t, uint64(2), r.cursor(t).SegmentNo)
	require.Equal(t, uint64(3), r.cursor(t).RS)
	require.Equal(t, 3, r.journalCount(t), "each record exactly once: the re-read segment adds no duplicate")
}

func TestRelayIngest_TornSealSuccessorGoneKeepsTheStrictVerdict(t *testing.T) {
	r := newPeerRig(t)
	tornWithSuccessor(t, r)
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	gone := segFile(r, 2)
	ingest.InjectSegmentRead(in, func(p string) ([]byte, error) {
		if p == gone {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		}
		return os.ReadFile(p)
	})
	stats := poll(t, in)
	require.Len(t, stats.Stalls, 1)
	require.Equal(t, segment.CodeSegmentCorrupt, stats.Stalls[0].Code,
		"a successor that is not there is not a medium fault: the tail stays unexplained")
}

// readSite runs one read path with its target made unreadable and returns the
// code and reason that path reports.
type readSite struct {
	name string
	run  func(t *testing.T) (code, reason string)
}

func ingestStall(t *testing.T, r *peerRig) (string, string) {
	t.Helper()
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats := poll(t, in)
	require.Len(t, stats.Stalls, 1)
	return stats.Stalls[0].Code, stats.Stalls[0].Reason
}

func uncheckedReason(t *testing.T, r *peerRig) (string, string) {
	t.Helper()
	unread := func(string) (ingest.Position, error) { return ingest.Position{}, nil }
	rep, err := ingest.FindGaps(context.Background(), r.peersDir, selfPeer, unread, r.config().Keys)
	require.NoError(t, err)
	require.Empty(t, rep.Gaps)
	require.Len(t, rep.Unchecked, 1, "an unreadable segment is never reported as a clean walk")
	return "", rep.Unchecked[0].Reason
}

func probeStall(t *testing.T, r *peerRig) (string, string) {
	t.Helper()
	unread := func(string) (ingest.Position, error) { return ingest.Position{}, nil }
	stalls, err := ingest.ProbeMedium(r.peersDir, selfPeer, unread)
	require.NoError(t, err)
	require.Len(t, stalls, 1)
	return stalls[0].Code, stalls[0].Reason
}

func onePublished(t *testing.T) *peerRig {
	t.Helper()
	r := newPeerRig(t)
	r.publish(t, fleetKey, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "x"}))...)
	return r
}

func TestRelayMedium_EveryReadSiteClassifiesTheFault(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	sites := []readSite{
		{"poll lists the segments directory", func(t *testing.T) (string, string) {
			r := onePublished(t)
			denyAccess(t, r.segDir, 0o700)
			return ingestStall(t, r)
		}},
		{"poll opens a segment", func(t *testing.T) (string, string) {
			r := onePublished(t)
			denyAccess(t, segFile(r, 1), 0o600)
			return ingestStall(t, r)
		}},
		{"poll opens the successor of a torn seal", func(t *testing.T) (string, string) {
			r := newPeerRig(t)
			tornWithSuccessor(t, r)
			denyAccess(t, segFile(r, 2), 0o600)
			return ingestStall(t, r)
		}},
		{"doctor walk lists the segments directory", func(t *testing.T) (string, string) {
			r := onePublished(t)
			denyAccess(t, r.segDir, 0o700)
			return uncheckedReason(t, r)
		}},
		{"doctor walk opens a segment", func(t *testing.T) (string, string) {
			r := onePublished(t)
			denyAccess(t, segFile(r, 1), 0o600)
			return uncheckedReason(t, r)
		}},
		{"doctor walk opens the successor of a torn seal", func(t *testing.T) (string, string) {
			r := newPeerRig(t)
			tornWithSuccessor(t, r)
			denyAccess(t, segFile(r, 2), 0o600)
			return uncheckedReason(t, r)
		}},
		{"status probe lists the segments directory", func(t *testing.T) (string, string) {
			r := onePublished(t)
			denyAccess(t, r.segDir, 0o700)
			return probeStall(t, r)
		}},
		{"status probe opens a segment", func(t *testing.T) (string, string) {
			r := onePublished(t)
			denyAccess(t, segFile(r, 1), 0o600)
			return probeStall(t, r)
		}},
	}
	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			code, reason := s.run(t)
			if code != "" {
				require.Equal(t, segment.CodeMediumUnreadable, code)
			}
			require.Contains(t, reason, "RELAY_MEDIUM_UNREADABLE: cannot read peers/"+remotePeer+"/segments")
			require.Contains(t, reason, "permission denied")
			require.NotContains(t, reason, "CORRUPT")
			require.NotContains(t, reason, os.TempDir(), "no absolute host path in the message")
		})
	}
}

func TestProbeMedium_StartsWhereIngestWouldAndSkipsSelf(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	r := onePublished(t)
	r.publish(t, fleetKey, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "y"}))...)
	denyAccess(t, segFile(r, 1), 0o600)

	at := func(seg uint64) func(string) (ingest.Position, error) {
		return func(string) (ingest.Position, error) {
			return ingest.Position{SegmentNo: seg, RS: 1, PubEpoch: 1}, nil
		}
	}
	stalls, err := ingest.ProbeMedium(r.peersDir, selfPeer, at(1))
	require.NoError(t, err)
	require.Len(t, stalls, 1, "segment 1 is unread, so its fault shows")
	require.Equal(t, uint64(1), stalls[0].Segment)

	stalls, err = ingest.ProbeMedium(r.peersDir, selfPeer, at(2))
	require.NoError(t, err)
	require.Empty(t, stalls, "a segment the cursor has passed is not read, as ingest does not read it")

	require.NoError(t, os.Chmod(segFile(r, 1), 0o600))
	selfDir := filepath.Join(r.peersDir, selfPeer, "segments")
	require.NoError(t, os.MkdirAll(selfDir, 0o700))
	selfSeg := filepath.Join(selfDir, segment.FileName(1))
	require.NoError(t, os.WriteFile(selfSeg, []byte("x"), 0o600))
	denyAccess(t, selfSeg, 0o600)
	stalls, err = ingest.ProbeMedium(r.peersDir, selfPeer, at(0))
	require.NoError(t, err)
	require.Empty(t, stalls, "this peer's own directory is never read")

	stalls, err = ingest.ProbeMedium(r.peersDir, "bbbbbbbbbbbbbbbb", at(0))
	require.NoError(t, err)
	require.Len(t, stalls, 1, "control: another reader does read that directory")
	require.Equal(t, selfPeer, stalls[0].PeerID)
}

func TestProbeMedium_StopsAtAHeaderIngestWouldStallOn(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	r := onePublished(t)
	r.publish(t, fleetKey, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "y"}))...)
	require.NoError(t, os.WriteFile(segFile(r, 1), []byte("not a segment header"), 0o600))
	denyAccess(t, segFile(r, 2), 0o600)
	unread := func(string) (ingest.Position, error) { return ingest.Position{}, nil }

	stalls, err := ingest.ProbeMedium(r.peersDir, selfPeer, unread)
	require.NoError(t, err)
	require.Empty(t, stalls, "ingest stalls on segment 1's header and never reads segment 2")

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	polled := poll(t, in)
	require.Len(t, polled.Stalls, 1)
	require.Equal(t, uint64(1), polled.Stalls[0].Segment, "the poll agrees: it stopped on segment 1")
	require.NotEqual(t, segment.CodeMediumUnreadable, polled.Stalls[0].Code)
}
