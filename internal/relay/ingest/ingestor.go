// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// CodeAuthFail is the FR-21 §9 registry entry counted when a record
// fails authentication. A spike for one peer is the §8.2 foreign-write
// signal: something that is not that peer is writing its directory.
//
// It is a COUNTER, not a verdict. The condition that increments it also
// produces a frame verdict of its own — RELAY_SEGMENT_CORRUPT — and the
// reader stalls on that per §5.4. Minting a second verdict code for one
// condition would make status and doctor disagree about what happened,
// so this package returns no codes; Stats.AuthFailures carries the
// signal and the segment package owns the verdict.
const CodeAuthFail = "RELAY_AUTH_FAIL"

// segmentsSubdir is where a peer's segments live under its directory.
const segmentsSubdir = "segments"

// maxSegmentRead bounds one segment read. The format caps a segment far
// below this; the limit exists because the medium is writable by other
// software, and a reader must not size an allocation from a file whose
// length it did not choose.
const maxSegmentRead = 64 << 20

// peerIDPattern is the FR-21 §5.2 identity grammar.
var peerIDPattern = regexp.MustCompile(`^[0-9a-f]{16}(-[a-z0-9_-]{1,32})?$`)

// KeySelector resolves the key for a frame's declared epoch. It is an
// interface so ingest carries no opinion about where keys live; the
// epoch keyring satisfies it.
type KeySelector interface {
	For(epoch uint16) ([]byte, error)
}

// Store is the narrow store slice ingest needs.
//
// Deliberately narrow, like the publisher's: everything that makes an
// event real — dedupe, the journal insert, vector-clock merge, LWW,
// conflict recording, and the derived-state recomputes — already lives
// behind the shipped apply path, and this package's job is to feed that
// path rather than to reimplement any of it.
type Store interface {
	RelayIngestCursor(ctx context.Context, peerID string) (sqlite.RelayIngestPosition, error)
	AdvanceRelayIngestCursor(ctx context.Context, peerID string, pos sqlite.RelayIngestPosition) error
	WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error
	ResolveDisplayPathByUID(ctx context.Context, uid string) (string, error)
	JournalTail(ctx context.Context) (int64, error)
	InitHookScanFloorAtTail(ctx context.Context) error
}

// Config configures a poll over a relay's peer directories.
type Config struct {
	Store Store

	// PeersDir is the relay's peers/ directory.
	PeersDir string

	// SelfPeerID is this peer, whose own directory is skipped — a peer
	// ingesting what it just published would be a loop with extra steps.
	SelfPeerID string

	// Keys resolves the key for a frame's epoch; nil in the §8.3
	// unauthenticated mode. Exactly one of Keys and Unauthenticated is
	// set, so a keyring that failed to load can never quietly downgrade
	// a reader into accepting unauthenticated records.
	Keys            KeySelector
	Unauthenticated bool

	Logger *slog.Logger
}

// Quarantine records a record skipped under the §6.4 authenticated
// policy: its MAC verified, so a trusted peer sent something the
// validator refused.
type Quarantine struct {
	PeerID  string
	Segment uint64
	RS      uint64
	EventID string
	Reason  string
}

// Stall records a peer whose stream stopped and did not advance.
type Stall struct {
	PeerID  string
	Segment uint64
	Code    string
	Reason  string
}

// Stats is what one poll did, for `relay status` and doctor.
type Stats struct {
	Applied      int
	Quarantined  int
	AuthFailures int

	Quarantines []Quarantine
	Stalls      []Stall

	// ForeignEntries are names under peers/ that are not peer ids.
	// Reported, never parsed and never removed.
	ForeignEntries []string

	// BootstrapFloorInitialized reports that this poll filled a journal
	// that had been empty, so the hook scan floor was moved to the tail.
	BootstrapFloorInitialized bool
}

// Ingestor applies one relay's peer streams into the local store.
type Ingestor struct {
	cfg Config
	log *slog.Logger
}

// New builds an ingestor, refusing a configuration that could not poll.
func New(cfg Config) (*Ingestor, error) {
	if cfg.Store == nil {
		return nil, errors.New("relay ingest: store is required")
	}
	if !peerIDPattern.MatchString(cfg.SelfPeerID) {
		return nil, fmt.Errorf("relay ingest: peer id %q is malformed", cfg.SelfPeerID)
	}
	if cfg.Unauthenticated == (cfg.Keys != nil) {
		return nil, errors.New("relay ingest: exactly one of Keys and Unauthenticated must be set")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Ingestor{cfg: cfg, log: log}, nil
}

// IngestAll polls every peer directory once.
//
// A poll lists the relay's peers, then for each one reads only the
// segments its cursor has not passed. Nothing here reopens history, so
// the cost of a poll tracks new work (FR-21 §9).
func (in *Ingestor) IngestAll(ctx context.Context) (Stats, error) {
	peers, foreign, err := segment.ListPeers(in.cfg.PeersDir)
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{ForeignEntries: foreign}

	// A fill into an EMPTY journal is a bootstrap: what arrives is
	// history, not fresh work. Detected before the loop so the hook scan
	// floor can be moved to the tail afterwards — otherwise a first
	// attach fires a hook per imported event and wakes the whole fleet
	// at once.
	preTail, tailErr := in.cfg.Store.JournalTail(ctx)

	for _, peer := range peers {
		if peer.PeerID == in.cfg.SelfPeerID {
			continue
		}
		if err := in.ingestPeer(ctx, peer, &stats); err != nil {
			return stats, err
		}
	}

	if tailErr == nil && preTail == 0 && stats.Applied > 0 {
		if err := in.cfg.Store.InitHookScanFloorAtTail(ctx); err != nil {
			return stats, fmt.Errorf("initialize hook scan floor: %w", err)
		}
		stats.BootstrapFloorInitialized = true
	}
	return stats, nil
}

// ingestPeer reads one peer's unread segments.
//
// It returns an error only for LOCAL store failures, which affect every
// peer equally. A problem with one peer's directory on the medium —
// a planted symlink, an unreadable file, a foreign entry — is recorded
// as that peer's stall and the poll moves on. Letting one directory
// fail the whole poll would hand anyone who can write the medium a way
// to stop the entire fleet converging, which is a worse outcome than
// one peer going quiet.
func (in *Ingestor) ingestPeer(ctx context.Context, peer segment.PeerDir, stats *Stats) error {
	dir := filepath.Join(peer.Path, segmentsSubdir)
	segs, _, err := segment.ListSegments(dir)
	if err != nil {
		if os.IsNotExist(errors.Unwrap(err)) || os.IsNotExist(err) {
			// A peer directory without segments has published nothing.
			return nil
		}
		in.stall(stats, peer.PeerID, 0, segment.CodeOf(err), err)
		return nil
	}
	if len(segs) == 0 {
		return nil
	}
	newest := segs[len(segs)-1].No

	prev, err := in.cfg.Store.RelayIngestCursor(ctx, peer.PeerID)
	if err != nil {
		return err
	}
	position := Position{SegmentNo: prev.SegmentNo, RS: prev.RS, PubEpoch: prev.PubEpoch}

	for _, file := range Unread(segs, position) {
		outcome, stop := in.ingestSegment(ctx, peer.PeerID, file, file.No < newest, position, stats)
		next, moved := Next(position, outcome)
		if moved {
			if err := in.cfg.Store.AdvanceRelayIngestCursor(ctx, peer.PeerID,
				sqlite.RelayIngestPosition{
					SegmentNo: next.SegmentNo, RS: next.RS, PubEpoch: next.PubEpoch,
				}); err != nil {
				return err
			}
			position = next
		}
		if stop {
			// A stall is durable: nothing past it is read, this poll or
			// any later one, until the medium is repaired.
			return nil
		}
	}
	return nil
}

// ingestSegment reads and applies one segment, returning what the scan
// reached and whether the peer's stream stops here.
func (in *Ingestor) ingestSegment(ctx context.Context, peerID string, file segment.File, sealed bool, prev Position, stats *Stats) (Outcome, bool) {
	raw, err := readSegment(file.Path)
	if err != nil {
		in.stall(stats, peerID, file.No, segment.CodeOf(err), err)
		return Outcome{Verdict: err}, true
	}
	header, err := segment.ParseHeader(raw)
	if err != nil {
		in.stall(stats, peerID, file.No, segment.CodeOf(err), err)
		return Outcome{Verdict: err}, true
	}
	key, err := in.keyFor(header)
	if err != nil {
		in.stall(stats, peerID, file.No, segment.CodeOf(err), err)
		return Outcome{Verdict: err}, true
	}

	sc, err := segment.NewScanner(bytes.NewReader(raw), segment.ScanOptions{
		Sealed:       sealed,
		Key:          key,
		ExpectPeerID: peerID,
	})
	if err != nil {
		in.stall(stats, peerID, file.No, segment.CodeOf(err), err)
		return Outcome{Verdict: err}, true
	}

	out := Outcome{Header: header}
	applied := segment.Cursor{
		SegmentNo: header.SegmentNo, RS: prev.RS, PubEpoch: header.PubEpoch,
	}
	if header.PubEpoch != prev.PubEpoch {
		// A new publisher epoch restarts relay sequences at a declared
		// base, so the previous epoch's position says nothing about
		// where this segment begins.
		applied.RS = header.FirstRS - 1
	}
	for sc.Next() {
		rec := sc.Record()
		if rec.RS <= applied.RS {
			// Already applied in an earlier poll; the scan re-reads a
			// segment from its start when resuming inside it.
			continue
		}
		if stop := in.applyRecord(ctx, peerID, header, rec, stats); stop {
			// A policy stall banks what was APPLIED, not what was
			// delivered: the refused record itself must stay ahead of
			// the cursor so the next poll meets it again and the stall
			// is durable rather than silently stepped over.
			out.Reached = applied
			return out, true
		}
		applied.RS = rec.RS
	}
	out.Reached = applied
	out.Truncated = sc.Truncated()
	out.Verdict = sc.Err()

	if out.Verdict != nil {
		// A MAC failure is a corruption-class verdict like any other —
		// the reader stalls on it per §5.4 — and it ALSO increments the
		// §8.2 counter, because a spike for one peer means something
		// that is not that peer is writing its directory. One condition,
		// a verdict and a signal.
		if errors.Is(out.Verdict, segment.ErrMACMismatch) {
			stats.AuthFailures++
		}
		in.stall(stats, peerID, file.No, segment.CodeOf(out.Verdict), out.Verdict)
		return out, true
	}
	return out, false
}

// applyRecord runs the ingest gate and, if it passes, the shipped apply
// path. It reports whether the peer's stream must stop here.
//
// The FR-21 §6.4 asymmetry lives in this function's two exits. A record
// whose MAC verified came from a peer holding the fleet key, so a
// semantic failure is that peer sending garbage: it is skipped, loudly.
// Stalling a whole fleet on one malformed event from a trusted peer is
// the worse outcome. On an unauthenticated relay there is no way to tell
// a peer's bug from an attacker's crafted record, so the same failure
// stalls — skipping there would let anyone who can write the folder
// erase an event from history by making it unparseable.
func (in *Ingestor) applyRecord(ctx context.Context, peerID string, header segment.Header, rec segment.Record, stats *Stats) bool {
	var e model.SyncEvent
	if err := decodeEvent(rec.Payload, &e); err != nil {
		return in.refuse(stats, peerID, header, rec, "", err)
	}
	// The §5.1 envelope, run locally. On the hub topology these rules
	// ran on a server the fleet trusts; on a relay there is no server,
	// so the reader is the validator.
	if err := validator.Validate(&e, time.Now().UTC(), nil); err != nil {
		return in.refuse(stats, peerID, header, rec, e.EventID, err)
	}
	if err := in.checkUID(ctx, &e); err != nil {
		return in.refuse(stats, peerID, header, rec, e.EventID, err)
	}

	// Everything that makes the event real happens here: dedupe against
	// applied_events, the journal insert that makes hooks fire, the
	// vector-clock merge, LWW with conflict recording, and the
	// derived-state recomputes. Bypassing this path would mean
	// reimplementing all of it, differently.
	if err := in.cfg.Store.WithTx(ctx, func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(ctx, tx, &e)
	}); err != nil {
		return in.refuse(stats, peerID, header, rec, e.EventID, err)
	}
	stats.Applied++
	return false
}

// refuse applies the §6.4 policy to a record the gate rejected.
func (in *Ingestor) refuse(stats *Stats, peerID string, header segment.Header, rec segment.Record, eventID string, cause error) bool {
	if in.cfg.Unauthenticated {
		in.log.Error("relay ingest stalled on an unverifiable record",
			slog.String("peer", peerID), slog.Uint64("rs", rec.RS), slog.Any("error", cause))
		stats.Stalls = append(stats.Stalls, Stall{
			PeerID: peerID, Segment: header.SegmentNo,
			Reason: fmt.Sprintf("rs %d: %v", rec.RS, cause),
		})
		return true
	}
	in.log.Error("relay ingest quarantined a record from an authenticated peer",
		slog.String("peer", peerID), slog.Uint64("rs", rec.RS),
		slog.String("event_id", eventID), slog.Any("error", cause))
	stats.Quarantined++
	stats.Quarantines = append(stats.Quarantines, Quarantine{
		PeerID: peerID, Segment: header.SegmentNo, RS: rec.RS,
		EventID: eventID, Reason: cause.Error(),
	})
	return false
}

// checkUID applies the SYNC-DESIGN §14.6 rule to one event: the same uid
// at the same display path is an idempotent no-op, and the same uid at a
// different path is two different things wearing one identity — rejected
// loudly, never guessed.
func (in *Ingestor) checkUID(ctx context.Context, e *model.SyncEvent) error {
	if e.UID == "" {
		return nil
	}
	localPath, err := in.cfg.Store.ResolveDisplayPathByUID(ctx, e.UID)
	if errors.Is(err, model.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve uid %s: %w", e.UID, err)
	}
	if localPath != e.NodeID {
		return fmt.Errorf("uid %s is local %s but arrived as %s: %w",
			e.UID, localPath, e.NodeID, model.ErrConflict)
	}
	return nil
}

// stall records a peer's stream stopping.
func (in *Ingestor) stall(stats *Stats, peerID string, segNo uint64, code string, cause error) {
	in.log.Error("relay ingest stalled",
		slog.String("peer", peerID), slog.Uint64("segment", segNo),
		slog.String("code", code), slog.Any("error", cause))
	stats.Stalls = append(stats.Stalls, Stall{
		PeerID: peerID, Segment: segNo, Code: code, Reason: cause.Error(),
	})
}

// keyFor resolves the key a segment's records are authenticated with,
// selected by the epoch the frame declares (FR-21 D-R10).
func (in *Ingestor) keyFor(h segment.Header) ([]byte, error) {
	if !h.Authenticated() {
		return nil, nil
	}
	if in.cfg.Keys == nil {
		return nil, fmt.Errorf("%w: authenticated segment on an unauthenticated relay",
			segment.ErrUnauthenticatedKey)
	}
	return in.cfg.Keys.For(h.KeyEpoch)
}

// readSegment reads a whole segment under the same Lstat discipline the
// walker uses, in one open — which is what keeps a poll's file-open
// count at one per unread segment (FR-21 §9).
func readSegment(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("lstat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s", segment.ErrSymlink, path)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", segment.ErrForeignEntry, path)
	}
	f, err := os.Open(path) // #nosec G304 -- path comes from the Lstat-verified relay walker
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxSegmentRead))
}

// decodeEvent unmarshals a record payload into a sync event, refusing
// anything that is not one rather than applying a half-populated event.
func decodeEvent(payload []byte, e *model.SyncEvent) error {
	if err := json.Unmarshal(payload, e); err != nil {
		return fmt.Errorf("decode relay record payload: %w", err)
	}
	return nil
}
