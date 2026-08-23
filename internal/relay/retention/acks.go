// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package retention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// AcksFileName is the reader's published read positions (FR-21 §6.7).
const AcksFileName = "acks.json"

// acksFileMode is the permission for the ack file. It is not secret —
// it says only how far this peer has read — but it is authoritative for
// pruning, so it stays owner-writable.
const acksFileMode os.FileMode = 0o644

// peerIDPattern is the FR-21 §5.2 identity grammar.
var peerIDPattern = regexp.MustCompile(`^[0-9a-f]{16}(-[a-z0-9_-]{1,32})?$`)

// ErrAcksUnusable marks an ack file that cannot be read as one.
//
// It is deliberately distinct from "absent": an empty view and a
// corrupt one look identical to a pruner but mean opposite things, and
// treating damage as "acked nothing" would hold every writer's window
// open silently and forever.
var ErrAcksUnusable = errors.New("relay ack file is unusable")

// Acks is the file's content: each peer's position as this reader last
// published it.
type Acks map[string]Position

// CursorSource supplies a reader's true, durable positions.
//
// FR-21 §6.7 phrases the self-heal as re-deriving from applied_events.
// The durable ingest cursor IS that fact: it advances only after the
// transaction that applied the records commits, so it can never claim
// more than applied_events holds. Reading it is the same answer,
// without a scan.
type CursorSource interface {
	RelayIngestCursor(ctx context.Context, peerID string) (sqlite.RelayIngestPosition, error)
}

// ReadAcks loads a peer's published positions.
//
// An absent file is an empty view, not an error: a peer that has never
// acked simply blocks pruning, which is the safe direction.
func ReadAcks(dir string) (Acks, error) {
	path := filepath.Join(dir, AcksFileName)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Acks{}, nil
		}
		return nil, fmt.Errorf("lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s is a symlink", ErrAcksUnusable, path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrAcksUnusable, path)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- the fixed ack file name in the caller's peer directory
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var acks Acks
	if err := json.Unmarshal(body, &acks); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrAcksUnusable, path, err)
	}
	for peer := range acks {
		if !peerIDPattern.MatchString(peer) {
			return nil, fmt.Errorf("%w: %s names peer %q", ErrAcksUnusable, path, peer)
		}
	}
	return acks, nil
}

// WriteAcks publishes a reader's positions.
//
// It writes a temp file and renames it, which is a tidiness measure and
// nothing more: per ADR-005 the rename's atomicity is NOT load-bearing
// on this medium. A torn rename leaves a file the next ReconcileAcks
// repairs, and the only cost is that other peers pruned later than they
// could have.
func WriteAcks(dir string, acks Acks) error {
	for peer := range acks {
		if !peerIDPattern.MatchString(peer) {
			return fmt.Errorf("write relay acks: peer %q is malformed", peer)
		}
	}
	body, err := json.MarshalIndent(acks, "", "  ")
	if err != nil {
		return fmt.Errorf("encode relay acks: %w", err)
	}
	body = append(body, '\n')

	path := filepath.Join(dir, AcksFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, acksFileMode); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// ReconcileAcks brings a peer's published positions back in line with
// its durable ones, rewriting the file when they differ or when it
// cannot be read at all. It reports whether it rewrote.
//
// This is the whole of the §6.7 advisory contract: the ack file is a
// convenience for other peers' pruning, never a record anything depends
// on. Damage to it degrades how soon writers may prune and nothing
// else, which is why it can be repaired from local state without
// consulting the medium or any other peer.
func ReconcileAcks(ctx context.Context, dir string, peers []string, src CursorSource) (Acks, bool, error) {
	published, readErr := ReadAcks(dir)
	unusable := readErr != nil && errors.Is(readErr, ErrAcksUnusable)
	if readErr != nil && !unusable {
		return nil, false, readErr
	}

	truth := make(Acks, len(peers))
	for _, peer := range peers {
		pos, err := src.RelayIngestCursor(ctx, peer)
		if err != nil {
			// A database problem must never be written into the file as
			// a rewind: other peers would read it as this reader having
			// lost ground and hold their windows open.
			return nil, false, err
		}
		truth[peer] = Position{SegmentNo: pos.SegmentNo, RS: pos.RS}
	}

	if !unusable && sameAcks(published, truth) {
		return truth, false, nil
	}
	if err := WriteAcks(dir, truth); err != nil {
		return nil, false, err
	}
	return truth, true, nil
}

// sameAcks compares two views.
func sameAcks(a, b Acks) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		other, ok := b[k]
		if !ok || other != a[k] {
			return false
		}
	}
	return true
}
