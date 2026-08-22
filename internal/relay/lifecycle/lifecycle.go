// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package lifecycle implements the FR-21 relay operator commands that
// span the identity record and the key material: init, attach, and
// rotate-key.
//
// It coordinates two data layers that stay independent of each other —
// relay.json (the record a joining peer checks its assumptions against)
// and the epoch keyring (what a frame is authenticated with). Keeping
// them separate is deliberate: a reader authenticates from the epoch in
// the frame and never consults the record, which is what lets FR-21
// §5.6 say a corrupt record blocks attach and the operator commands
// while data already flowing between attached peers continues.
//
// Every command here fails closed. An attach does not complete until
// the key it will need is in hand, a mode mismatch is refused rather
// than resolved, and a divergent history is refused rather than merged.
// There is no server to appeal to on a relay, so a command that
// half-succeeds leaves an operator with no way to tell what happened.
//
// The package is pure: standard library only, no store imports, no
// goroutines. Identity, time, and randomness all arrive from the
// caller.
package lifecycle

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
)

// Verdict codes surfaced to operators by attach and the lifecycle
// commands.
const (
	// CodeModeMismatch marks an authenticated peer meeting an
	// unauthenticated relay, or the reverse (FR-21 §8.3).
	CodeModeMismatch = "RELAY_MODE_MISMATCH"

	// CodeHistoryDiverged marks a shared project whose first event
	// differs — two histories wearing one prefix (FR-21 §6.5).
	CodeHistoryDiverged = "RELAY_HISTORY_DIVERGED"

	// CodeNoSharedProject marks a peer with nothing in common with the
	// relay it is attaching to.
	CodeNoSharedProject = "RELAY_NO_SHARED_PROJECT"
)

// Refusals. Callers dispatch with errors.Is.
var (
	// ErrModeMismatch is RELAY_MODE_MISMATCH.
	ErrModeMismatch = errors.New(CodeModeMismatch + ": relay and peer disagree on authentication")

	// ErrHistoryDiverged is RELAY_HISTORY_DIVERGED.
	ErrHistoryDiverged = errors.New(CodeHistoryDiverged + ": shared project has a different history")

	// ErrNoSharedProject is RELAY_NO_SHARED_PROJECT.
	ErrNoSharedProject = errors.New(CodeNoSharedProject + ": no project in common with this relay")
)

// CodeOf returns this package's RELAY_* code for a verdict, or "".
// Callers surfacing a verdict from a lifecycle command should also
// consult the metadata and keyring packages, which own their own codes.
func CodeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrModeMismatch):
		return CodeModeMismatch
	case errors.Is(err, ErrHistoryDiverged):
		return CodeHistoryDiverged
	case errors.Is(err, ErrNoSharedProject):
		return CodeNoSharedProject
	default:
		return ""
	}
}

// InitRequest is what `relay init` needs.
type InitRequest struct {
	// RelayDir is the relay directory; RelayID, CreatedAt, CreatedBy
	// and Projects populate the record.
	RelayDir  string
	KeysDir   string
	RelayID   string
	CreatedAt time.Time
	CreatedBy string
	Projects  []metadata.Project

	// Authenticated selects the default mode. False is the §8.3 opt-out
	// and mints no key at all.
	Authenticated bool

	// Rand is the randomness source for the first key
	// (crypto/rand.Reader in production).
	Rand io.Reader
}

// InitResult reports what init created.
type InitResult struct {
	Relay *metadata.Relay

	// KeyEpoch is the epoch whose key was installed, or zero on an
	// unauthenticated relay.
	KeyEpoch uint16
}

// Init creates a relay: the identity record, and the first key when the
// relay is authenticated.
//
// The key is written before the record. A relay whose record exists but
// whose key does not would be attachable and unusable — peers would
// complete a handshake and then fail to authenticate anything — whereas
// key material with no record is simply not yet a relay, and the retry
// that follows finds nothing to trip over.
func Init(req InitRequest) (*InitResult, error) {
	// Check for an existing relay before minting anything, so re-running
	// init on a live relay says so rather than reporting whichever
	// artifact it happened to collide with first. metadata.Write still
	// refuses exclusively — this check is for the message, not for the
	// guarantee.
	if _, err := os.Lstat(filepath.Join(req.RelayDir, metadata.FileName)); err == nil {
		return nil, fmt.Errorf("%w: %s", metadata.ErrRelayExists, req.RelayDir)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("lstat %s: %w", req.RelayDir, err)
	}

	doc, err := metadata.Init(metadata.InitConfig{
		RelayID:       req.RelayID,
		CreatedAt:     req.CreatedAt,
		CreatedBy:     req.CreatedBy,
		Projects:      req.Projects,
		Authenticated: req.Authenticated,
	})
	if err != nil {
		return nil, err
	}

	res := &InitResult{Relay: doc}
	if req.Authenticated {
		key, genErr := keyring.Generate(req.Rand)
		if genErr != nil {
			return nil, genErr
		}
		if writeErr := keyring.Write(req.KeysDir, metadata.FirstKeyEpoch, key); writeErr != nil {
			return nil, writeErr
		}
		res.KeyEpoch = metadata.FirstKeyEpoch
	}
	if err := metadata.Write(req.RelayDir, doc); err != nil {
		return nil, err
	}
	return res, nil
}

// AttachRequest is what a joining peer knows locally.
type AttachRequest struct {
	RelayDir string
	KeysDir  string

	// Local is the joining store's projects with their history anchors.
	Local []metadata.Project

	// Authenticated is the mode this peer is configured for. It is
	// declared rather than inferred from the relay: inferring it would
	// let a relay silently downgrade a peer that was configured to
	// require authentication.
	Authenticated bool
}

// AttachResult reports what a successful attach agreed to.
type AttachResult struct {
	Relay *metadata.Relay

	// SharedProjects are the prefixes this peer and the relay have in
	// common, sorted.
	SharedProjects []string

	// KeyEpoch is the epoch this peer will publish under, or zero on an
	// unauthenticated relay.
	KeyEpoch uint16
}

// Attach runs the FR-21 §6.5 handshake.
//
// It verifies, in order: the record is readable (a corrupt one blocks
// attach, §5.6); the modes agree (§8.3); at least one project is
// shared; every shared project's history anchor matches; and — for an
// authenticated relay — the key for the current epoch is already
// present. That last check is what makes the handshake fail closed:
// attaching first and discovering the key later would leave a peer
// configured for a relay it cannot read, publishing nothing and
// reporting no reason.
func Attach(req AttachRequest) (*AttachResult, error) {
	doc, err := metadata.Read(req.RelayDir)
	if err != nil {
		return nil, err
	}
	if doc.Authenticated != req.Authenticated {
		return nil, fmt.Errorf("%w: relay is %s, this peer is configured %s",
			ErrModeMismatch, authWord(doc.Authenticated), authWord(req.Authenticated))
	}

	shared, err := sharedProjects(doc.Projects, req.Local)
	if err != nil {
		return nil, err
	}

	res := &AttachResult{Relay: doc, SharedProjects: shared}
	if !doc.Authenticated {
		return res, nil
	}
	epoch, ok := doc.CurrentKeyEpoch()
	if !ok {
		return nil, fmt.Errorf("%w: relay records no key epoch", metadata.ErrRelayCorrupt)
	}
	ring, err := keyring.Load(req.KeysDir)
	if err != nil {
		return nil, err
	}
	if _, err := ring.For(epoch); err != nil {
		return nil, err
	}
	res.KeyEpoch = epoch
	return res, nil
}

// authWord renders a mode for an operator-facing message.
func authWord(authenticated bool) string {
	if authenticated {
		return "authenticated"
	}
	return "unauthenticated"
}

// sharedProjects intersects the relay's projects with the peer's,
// refusing a divergent history on any project they share.
//
// The history check is the reason a prefix alone is not enough: two
// stores whose first events differ are different projects wearing one
// name, and merging them would interleave unrelated work under one set
// of ids.
func sharedProjects(relay, local []metadata.Project) ([]string, error) {
	byPrefix := make(map[string]string, len(local))
	for _, p := range local {
		byPrefix[p.Prefix] = p.FirstEventHash
	}
	var shared []string
	for _, p := range relay {
		localHash, ok := byPrefix[p.Prefix]
		if !ok {
			continue
		}
		if localHash != p.FirstEventHash {
			return nil, fmt.Errorf("%w: project %q", ErrHistoryDiverged, p.Prefix)
		}
		shared = append(shared, p.Prefix)
	}
	if len(shared) == 0 {
		return nil, ErrNoSharedProject
	}
	sort.Strings(shared)
	return shared, nil
}

// RotateRequest is what `relay rotate-key` needs.
type RotateRequest struct {
	RelayDir string
	KeysDir  string

	// FromRSByPeer records where the new epoch takes effect in each
	// peer's stream. Per-peer because relay sequences are per-peer.
	FromRSByPeer map[string]uint64

	// Rand is the randomness source for the new key.
	Rand io.Reader
}

// RotateResult reports the epoch the rotation established.
type RotateResult struct {
	Relay    *metadata.Relay
	KeyEpoch uint16
}

// RotateKey re-keys the relay from a chosen point (FR-21 §8.2, D-R10).
//
// The next epoch's key is installed first and the boundary recorded
// second, and the operation is re-runnable from either half-state. That
// matters more here than it looks: these fleets rotate over sneakernet,
// where an interrupted command may not be retried for days, and a
// rotation that could only be half-completed would leave the relay
// wedged with no way forward that does not destroy key material.
//
// Existing epochs are never disturbed. Replacing an installed key would
// invalidate every record already published under it, and moving a
// recorded boundary would relocate a line readers had already crossed.
func RotateKey(req RotateRequest) (*RotateResult, error) {
	doc, err := metadata.Read(req.RelayDir)
	if err != nil {
		return nil, err
	}
	if !doc.Authenticated {
		return nil, fmt.Errorf("rotate key: relay is unauthenticated and has no keys to rotate")
	}
	current, ok := doc.CurrentKeyEpoch()
	if !ok {
		return nil, fmt.Errorf("%w: relay records no key epoch", metadata.ErrRelayCorrupt)
	}
	next := current + 1
	if next < current {
		return nil, fmt.Errorf("rotate key: key epoch %d is the last this format can carry", current)
	}

	if err := ensureEpochKey(req.KeysDir, next, req.Rand); err != nil {
		return nil, err
	}
	if err := doc.AppendKeyEpoch(next, req.FromRSByPeer); err != nil {
		return nil, err
	}
	if err := metadata.Rewrite(req.RelayDir, doc); err != nil {
		return nil, err
	}
	return &RotateResult{Relay: doc, KeyEpoch: next}, nil
}

// ensureEpochKey installs the key for an epoch, tolerating one that a
// previous interrupted run already wrote.
//
// The existing material is kept rather than replaced: anything already
// published under that epoch must stay verifiable, and a rotation that
// destroyed key material on retry would be a worse failure than the one
// it was recovering from.
func ensureEpochKey(keysDir string, epoch uint16, randSource io.Reader) error {
	ring, loadErr := keyring.Load(keysDir)
	switch {
	case loadErr == nil:
		if _, forErr := ring.For(epoch); forErr == nil {
			return nil
		}
	case !errors.Is(loadErr, keyring.ErrKeyAbsent):
		return loadErr
	}
	key, err := keyring.Generate(randSource)
	if err != nil {
		return err
	}
	if err := keyring.Write(keysDir, epoch, key); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}
