// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/relay/segment"
)

// mediumErr classifies an I/O or permission failure on path as
// RELAY_MEDIUM_UNREADABLE (FR-21 §9), naming the path relative to the relay
// directory (the parent of peersDir). Not-exist and errors that already carry
// a verdict pass through unchanged.
func mediumErr(peersDir, path string, err error) error {
	rel, relErr := filepath.Rel(filepath.Dir(peersDir), path)
	if relErr != nil {
		rel = filepath.Base(path)
	}
	return segment.MediumError(filepath.ToSlash(rel), err)
}

// mediumVerdict is mediumErr narrowed to a medium fault: it returns the
// RELAY_MEDIUM_UNREADABLE error for err on path, or nil when err is not one
// (not-exist, or an error that already carries its own verdict).
func mediumVerdict(peersDir, path string, err error) error {
	if e := mediumErr(peersDir, path, err); segment.CodeOf(e) == segment.CodeMediumUnreadable {
		return e
	}
	return nil
}

// unreadableReason is the doctor's Unchecked reason for a path that could not
// be read: the relay-relative RELAY_MEDIUM_UNREADABLE text when it is a medium
// fault (never an absolute host path), else what: err.
func unreadableReason(peersDir, path, what string, err error) string {
	if mv := mediumVerdict(peersDir, path, err); mv != nil {
		return what + ": " + mv.Error()
	}
	return what + ": " + err.Error()
}

// ProbeMedium is the read-only check behind `relay status` and doctor: it
// lists each peer's segments and opens the ones the cursor has not passed
// (Unread, the same start a poll uses), and reports every peer whose medium
// cannot be read. Like a poll it stops at the first segment whose header
// cannot be parsed, because ingest stalls there with that verdict and never
// reads past it. It judges readability only: a verdict inside a segment body
// (damaged records, a gap) is what `relay tick` and the continuity row name.
// It applies nothing and moves no cursor. The returned stalls carry
// RELAY_MEDIUM_UNREADABLE; a peer that reads cleanly is absent, which is how
// the code clears.
func ProbeMedium(peersDir, selfPeerID string, cursor func(peerID string) (Position, error)) ([]Stall, error) {
	peers, _, err := segment.ListPeers(peersDir)
	if err != nil {
		return nil, err
	}
	var out []Stall
	for _, peer := range peers {
		if peer.PeerID == selfPeerID {
			continue
		}
		pos, curErr := cursor(peer.PeerID)
		if curErr != nil {
			return nil, curErr
		}
		if s := probePeer(peersDir, peer, pos); s != nil {
			out = append(out, *s)
		}
	}
	return out, nil
}

// probePeer reports the first unreadable path of one peer, or nil.
func probePeer(peersDir string, peer segment.PeerDir, pos Position) *Stall {
	dir := filepath.Join(peer.Path, segmentsSubdir)
	segs, _, err := segment.ListSegments(dir)
	if err != nil {
		return mediumStall(peersDir, peer.PeerID, 0, dir, err)
	}
	for _, f := range Unread(segs, pos) {
		raw, err := readSegment(f.Path)
		if err != nil {
			return mediumStall(peersDir, peer.PeerID, f.No, f.Path, err)
		}
		if _, err := segment.ParseHeader(raw); err != nil {
			return nil // ingest stalls here with its own verdict; nothing past it is read
		}
	}
	return nil
}

// mediumStall builds the stall for err when it is a medium fault, else nil.
func mediumStall(peersDir, peerID string, segNo uint64, path string, err error) *Stall {
	err = mediumErr(peersDir, path, err)
	if segment.CodeOf(err) != segment.CodeMediumUnreadable {
		return nil
	}
	return &Stall{PeerID: peerID, Segment: segNo, Code: segment.CodeMediumUnreadable, Reason: err.Error()}
}
