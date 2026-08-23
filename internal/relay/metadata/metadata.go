// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package metadata owns relay.json, the FR-21 relay's identity record.
//
// The file is written once at init and never rewritten (FR-21 §5.6).
// Exactly two of its fields may grow afterwards — key_epochs, the
// rotation boundary history that makes D-R10 auditable, and
// retired_peers, the roster that keeps a vanished peer from stalling
// everyone's pruning. Every other field is fixed at creation, and
// Rewrite refuses a document whose fixed part has moved.
//
// The two grow differently, deliberately. key_epochs is history — it
// records where a rotation boundary fell, and an entry that moved would
// relocate a line readers have already crossed — so it is strictly
// append-only. retired_peers is a live roster: an operator retires a
// vanished peer, and that peer clears its own entry when it rejoins
// through bootstrap (§6.7), so entries may leave as well as arrive.
//
// That asymmetry is the point: a relay has no server to arbitrate, so
// the only thing a joining peer can check its assumptions against is
// this record. A file that could be rewritten could quietly re-point a
// fleet at a different history, or downgrade it out of authentication.
// Corruption here therefore blocks attach and the operator commands and
// nothing else — peers already attached verified a copy into their own
// configuration and do not consult it to move data.
//
// The package is pure: standard library only, no store imports, no
// goroutines. It mints neither identity nor timestamps — a caller
// supplies the relay id, the creation time, and the project set, which
// keeps the record reproducible and this layer free of a clock.
package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// FileName is the relay identity record's name inside the relay
// directory.
const FileName = "relay.json"

// FormatVersion is the relay format major this build writes and reads.
// It is the same major the segment frames carry (FR-21 §10): a peer
// that cannot read one cannot read the other.
const FormatVersion = 1

// FirstKeyEpoch is the epoch an authenticated relay starts at. Zero is
// left unused so a frame that names epoch 0 reads as "no key for that
// epoch" rather than matching a relay's first key by accident.
const FirstKeyEpoch uint16 = 1

// fileMode is the permission for relay.json. The record is not secret —
// the key material lives elsewhere — but it is authoritative, so it is
// written owner-writable rather than group-writable.
const fileMode os.FileMode = 0o644

// peerIDPattern is the FR-21 §5.2 identity grammar. It is restated here
// rather than imported: this package carries no dependency on the frame
// layer, and the grammar is part of the record's own validity.
var peerIDPattern = regexp.MustCompile(`^[0-9a-f]{16}(-[a-z0-9_-]{1,32})?$`)

// Verdict codes surfaced to operators.
const (
	// CodeMetaAbsent marks a directory that is not a relay.
	CodeMetaAbsent = "RELAY_META_ABSENT"

	// CodeMetaCorrupt marks an unreadable or invalid identity record.
	CodeMetaCorrupt = "RELAY_META_CORRUPT"

	// CodeMetaSymlink marks a symlink in the record's path (CWE-59).
	CodeMetaSymlink = "RELAY_META_SYMLINK"
)

// Verdicts and refusals. Callers dispatch with errors.Is.
var (
	// ErrRelayAbsent is RELAY_META_ABSENT.
	ErrRelayAbsent = errors.New(CodeMetaAbsent + ": no relay record in this directory")

	// ErrRelayCorrupt is RELAY_META_CORRUPT.
	ErrRelayCorrupt = errors.New(CodeMetaCorrupt + ": relay record is unreadable")

	// ErrRelaySymlink is RELAY_META_SYMLINK.
	ErrRelaySymlink = errors.New(CodeMetaSymlink + ": symlink in the relay record path")

	// ErrRelayExists refuses to initialize over an existing relay.
	ErrRelayExists = errors.New("relay record already exists")

	// ErrRelayImmutable refuses a change to a field fixed at init.
	ErrRelayImmutable = errors.New("relay record field is immutable after init")

	// ErrEpochNotForward refuses a key epoch that does not advance.
	ErrEpochNotForward = errors.New("key epoch does not advance the recorded history")
)

// CodeOf returns the RELAY_* code a verdict carries, or "".
func CodeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrRelaySymlink):
		return CodeMetaSymlink
	case errors.Is(err, ErrRelayAbsent):
		return CodeMetaAbsent
	case errors.Is(err, ErrRelayCorrupt):
		return CodeMetaCorrupt
	default:
		return ""
	}
}

// Project is one project carried by the relay, with the divergent
// history check's anchor.
type Project struct {
	// Prefix is the project's display prefix.
	Prefix string `json:"project_prefix"`

	// FirstEventHash anchors the project's history. Two stores whose
	// first events differ are different histories wearing one prefix,
	// which attach refuses rather than merges.
	FirstEventHash string `json:"first_event_hash"`
}

// KeyEpochBoundary records where a key epoch takes effect, per peer.
//
// It is per-peer because relay sequences are per-peer: one fleet-wide
// number could not say where the boundary falls in each publisher's
// stream. The record is an audit trail — a reader authenticates from
// the epoch in the frame (D-R10), never from this file — which is
// exactly why the file may be corrupt without stopping data flow.
type KeyEpochBoundary struct {
	// Epoch is the key epoch.
	Epoch uint16 `json:"epoch"`

	// FromRSByPeer maps peer id to the relay sequence at which that
	// peer began publishing under this epoch.
	FromRSByPeer map[string]uint64 `json:"from_rs_by_peer,omitempty"`
}

// Relay is the identity record itself.
type Relay struct {
	FormatVersion int                `json:"format_version"`
	RelayID       string             `json:"relay_id"`
	CreatedAt     time.Time          `json:"created_at"`
	CreatedBy     string             `json:"created_by"`
	Projects      []Project          `json:"projects"`
	Authenticated bool               `json:"authenticated"`
	KeyEpochs     []KeyEpochBoundary `json:"key_epochs"`
	RetiredPeers  []string           `json:"retired_peers"`
}

// InitConfig is what `relay init` needs from its caller.
type InitConfig struct {
	// RelayID identifies the relay. The caller mints it (a UUIDv7 in
	// production) so this package needs no randomness source.
	RelayID string

	// CreatedAt is the creation time, supplied rather than read from a
	// clock, per the project's clock-injection rule.
	CreatedAt time.Time

	// CreatedBy is the initializing peer, per the §5.2 grammar.
	CreatedBy string

	// Projects are the projects the relay carries.
	Projects []Project

	// Authenticated selects the default mode. False is the §8.3 opt-out
	// and is recorded so a later attach refuses a mode mismatch instead
	// of inferring one.
	Authenticated bool
}

// Init builds the record `relay init` writes.
func Init(cfg InitConfig) (*Relay, error) {
	doc := &Relay{
		FormatVersion: FormatVersion,
		RelayID:       cfg.RelayID,
		CreatedAt:     cfg.CreatedAt.UTC(),
		CreatedBy:     cfg.CreatedBy,
		Projects:      append([]Project(nil), cfg.Projects...),
		Authenticated: cfg.Authenticated,
		RetiredPeers:  []string{},
		KeyEpochs:     []KeyEpochBoundary{},
	}
	if cfg.CreatedAt.IsZero() {
		return nil, fmt.Errorf("relay init: creation time is required")
	}
	if doc.Authenticated {
		// The relay starts under its first key; the boundary is the
		// start of every peer's stream, so it carries no per-peer
		// positions.
		doc.KeyEpochs = append(doc.KeyEpochs, KeyEpochBoundary{Epoch: FirstKeyEpoch})
	}
	if err := doc.validate(); err != nil {
		return nil, fmt.Errorf("relay init: %w", err)
	}
	return doc, nil
}

// validate checks the invariants a record must hold to be usable.
func (r *Relay) validate() error {
	if r.FormatVersion != FormatVersion {
		return fmt.Errorf("format version %d, this build writes %d", r.FormatVersion, FormatVersion)
	}
	if r.RelayID == "" {
		return errors.New("relay id is required")
	}
	if !peerIDPattern.MatchString(r.CreatedBy) {
		return fmt.Errorf("creating peer %q is not a valid peer id", r.CreatedBy)
	}
	if len(r.Projects) == 0 {
		return errors.New("at least one project is required")
	}
	seen := make(map[string]bool, len(r.Projects))
	for _, p := range r.Projects {
		switch {
		case p.Prefix == "":
			return errors.New("a project has no prefix")
		case p.FirstEventHash == "":
			return fmt.Errorf("project %q has no first event hash", p.Prefix)
		case seen[p.Prefix]:
			return fmt.Errorf("project %q appears twice", p.Prefix)
		}
		seen[p.Prefix] = true
	}
	return nil
}

// CurrentKeyEpoch returns the newest recorded key epoch. The second
// result is false on an unauthenticated relay, which has none.
func (r *Relay) CurrentKeyEpoch() (uint16, bool) {
	if len(r.KeyEpochs) == 0 {
		return 0, false
	}
	return r.KeyEpochs[len(r.KeyEpochs)-1].Epoch, true
}

// AppendKeyEpoch records a rotation boundary.
//
// The epoch must advance the recorded history. Re-stating one at a
// different relay sequence would move a boundary readers have already
// crossed, which is the single way this record could lie about a
// rotation — so it is refused rather than merged.
func (r *Relay) AppendKeyEpoch(epoch uint16, fromRSByPeer map[string]uint64) error {
	if !r.Authenticated {
		return errors.New("append key epoch: relay is unauthenticated and has no keys to rotate")
	}
	current, ok := r.CurrentKeyEpoch()
	if !ok || epoch <= current {
		return fmt.Errorf("%w: epoch %d does not follow %d", ErrEpochNotForward, epoch, current)
	}
	for peer := range fromRSByPeer {
		if !peerIDPattern.MatchString(peer) {
			return fmt.Errorf("append key epoch: peer %q is not a valid peer id", peer)
		}
	}
	boundary := KeyEpochBoundary{Epoch: epoch}
	if len(fromRSByPeer) > 0 {
		boundary.FromRSByPeer = make(map[string]uint64, len(fromRSByPeer))
		for peer, rs := range fromRSByPeer {
			boundary.FromRSByPeer[peer] = rs
		}
	}
	r.KeyEpochs = append(r.KeyEpochs, boundary)
	return nil
}

// Read loads and validates the record in dir.
func Read(dir string) (*Relay, error) {
	path := filepath.Join(dir, FileName)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrRelayAbsent, path)
		}
		return nil, fmt.Errorf("lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s", ErrRelaySymlink, path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrRelayCorrupt, path)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- path is the fixed record name in the caller's relay directory
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var doc Relay
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrRelayCorrupt, path, err)
	}
	if err := doc.validate(); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrRelayCorrupt, path, err)
	}
	return &doc, nil
}

// Write creates the record. It refuses to overwrite: relay.json is
// written once, and an init over a live relay would orphan every peer
// already attached to it.
func Write(dir string, doc *Relay) error {
	if err := doc.validate(); err != nil {
		return fmt.Errorf("write relay record: %w", err)
	}
	body, err := marshal(doc)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, FileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode) // #nosec G304 -- path is the fixed record name in the caller's relay directory
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%w: %s", ErrRelayExists, path)
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	_, writeErr := f.Write(body)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Rewrite persists an append-only change to an existing record.
//
// It re-reads what is on the medium and refuses unless every field
// fixed at init still matches and the two append-permitted arrays have
// only grown. This is where §5.6's immutability is actually enforced —
// an in-memory struct can be edited freely, so the check belongs at the
// boundary where the edit would become durable.
func Rewrite(dir string, doc *Relay) error {
	existing, err := Read(dir)
	if err != nil {
		return err
	}
	// Immutability is checked before validity so that every fixed field
	// refuses the same way. A changed format_version is a rewrite of a
	// field fixed at init, and reporting it as an unsupported format
	// would send the reader looking for a version problem that is not
	// there.
	// checkAppendOnly compares every field validate would check against
	// a record Read already validated, so a document that passes it is
	// valid by construction — there is no second validation to run here
	// that could ever fire.
	if appendErr := existing.checkAppendOnly(doc); appendErr != nil {
		return appendErr
	}
	body, err := marshal(doc)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, body, fileMode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// checkAppendOnly reports whether next is a legal successor of r.
func (r *Relay) checkAppendOnly(next *Relay) error {
	switch {
	case next.FormatVersion != r.FormatVersion:
		return fmt.Errorf("%w: format_version", ErrRelayImmutable)
	case next.RelayID != r.RelayID:
		return fmt.Errorf("%w: relay_id", ErrRelayImmutable)
	case !next.CreatedAt.Equal(r.CreatedAt):
		return fmt.Errorf("%w: created_at", ErrRelayImmutable)
	case next.CreatedBy != r.CreatedBy:
		return fmt.Errorf("%w: created_by", ErrRelayImmutable)
	case next.Authenticated != r.Authenticated:
		return fmt.Errorf("%w: authenticated", ErrRelayImmutable)
	case len(next.Projects) != len(r.Projects):
		return fmt.Errorf("%w: projects", ErrRelayImmutable)
	}
	for i, p := range r.Projects {
		if next.Projects[i] != p {
			return fmt.Errorf("%w: projects", ErrRelayImmutable)
		}
	}
	if len(next.KeyEpochs) < len(r.KeyEpochs) {
		return fmt.Errorf("%w: key_epochs may only grow", ErrRelayImmutable)
	}
	for i, e := range r.KeyEpochs {
		if !sameBoundary(next.KeyEpochs[i], e) {
			return fmt.Errorf("%w: key_epochs entry %d", ErrRelayImmutable, i)
		}
	}
	// retired_peers is a live ROSTER, not history: retire-peer adds to
	// it and a peer rejoining through bootstrap clears its own entry
	// (FR-21 §6.7), so both directions are permitted. key_epochs above
	// is the opposite — it records where a rotation boundary fell, and
	// moving one would relocate a line readers have already crossed.
	for _, p := range next.RetiredPeers {
		if !peerIDPattern.MatchString(p) {
			return fmt.Errorf("%w: retired_peers names %q", ErrRelayImmutable, p)
		}
	}
	return nil
}

// sameBoundary compares two recorded boundaries.
func sameBoundary(a, b KeyEpochBoundary) bool {
	if a.Epoch != b.Epoch || len(a.FromRSByPeer) != len(b.FromRSByPeer) {
		return false
	}
	for peer, rs := range b.FromRSByPeer {
		if a.FromRSByPeer[peer] != rs {
			return false
		}
	}
	return true
}

// marshal renders the record deterministically.
//
// Go's JSON encoder already sorts map keys, and the struct field order
// is fixed, so the same record always produces the same bytes — the
// file stays diffable, and two peers writing it never disagree over
// formatting alone.
func marshal(doc *Relay) ([]byte, error) {
	sorted := *doc
	sorted.KeyEpochs = append([]KeyEpochBoundary(nil), doc.KeyEpochs...)
	sort.Slice(sorted.KeyEpochs, func(i, j int) bool {
		return sorted.KeyEpochs[i].Epoch < sorted.KeyEpochs[j].Epoch
	})
	body, err := json.MarshalIndent(&sorted, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode relay record: %w", err)
	}
	return append(body, '\n'), nil
}
