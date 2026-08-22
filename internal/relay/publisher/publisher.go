// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package publisher is the FR-21 relay publish path: it reads this
// peer's journal past a durable cursor, frames each event into the
// peer's own segment directory, and advances the cursor behind it.
//
// This is the integration layer. The frame, key, record, and lifecycle
// packages beneath it stay store-free; this package is where the
// journal meets the medium, and it reaches the store through the
// narrow interface below rather than the full store surface.
//
// Two contracts govern everything here, and both are about what must
// NOT happen:
//
//   - The cursor advances only after the append returns (FR-21 §6.2).
//     A crash in between republishes, and the duplicate is absorbed
//     downstream by event dedupe. The reverse ordering would mark
//     events published that no reader can ever receive.
//   - A publish failure is logged, counted, and retried next tick. It
//     must never fail the user's mutation and never trip the store's
//     fail-stop latch: that latch guards store integrity, and a dead
//     relay is degraded transport, not a damaged store.
package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync/atomic"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// CodePublisherDiverged is the FR-21 §9 registry code for a publisher
// whose journal no longer matches what it has already published.
const CodePublisherDiverged = "RELAY_PUBLISHER_DIVERGED"

// ErrPublisherDiverged is RELAY_PUBLISHER_DIVERGED (FR-21 §5.7).
var ErrPublisherDiverged = errors.New(CodePublisherDiverged +
	": this peer's journal no longer matches what it published")

// CodeOf returns the RELAY_* code a verdict carries, or "". Callers
// should also consult the segment package, which owns the frame codes.
func CodeOf(err error) string {
	if err != nil && errors.Is(err, ErrPublisherDiverged) {
		return CodePublisherDiverged
	}
	return ""
}

// peerIDPattern is the FR-21 §5.2 identity grammar.
var peerIDPattern = regexp.MustCompile(`^[0-9a-f]{16}(-[a-z0-9_-]{1,32})?$`)

// defaultBatchLimit bounds one tick's journal read. A tick's cost
// tracks what is new, never the length of history (FR-21 §9).
const defaultBatchLimit = 500

// tailVerifyDepth is how many of this peer's own most recent published
// records the §5.7 startup check examines. A handful is enough: a
// restore rewinds the journal wholesale, so the divergence shows up
// immediately at the tail, and reading more would cost every startup to
// catch nothing extra.
const tailVerifyDepth = 8

// Journal is the store slice the publisher needs. It is deliberately
// narrow — the same shape as the inbox reader elsewhere in the tree —
// so the publish path can be driven by a fake and so this package never
// reaches for store surface it has no business touching.
type Journal interface {
	RelayPushCursor(ctx context.Context) (sqlite.RelayPushPosition, error)
	AdvanceRelayPushCursor(ctx context.Context, seq int64, nextRS uint64) error
	ResetRelayPublisher(ctx context.Context, floorSeq int64, baseRS uint64) error
	ReadRelayJournalSince(ctx context.Context, seq int64, limit int) ([]sqlite.RelayJournalEvent, error)
	LookupRelayJournalSeqs(ctx context.Context, eventIDs []string) (map[string]int64, error)
}

// Config configures a publisher for one peer's own segment directory.
type Config struct {
	Journal     Journal
	SegmentsDir string
	PeerID      string

	// Key is the fleet MAC key; Unauthenticated selects the §8.3 mode.
	// Exactly one of them is set, so a key that failed to load can
	// never silently downgrade a fleet to unauthenticated publishing.
	Key             []byte
	Unauthenticated bool
	KeyEpoch        uint16

	// MaxSegmentBytes is the rotation threshold; zero takes the default.
	MaxSegmentBytes int64

	// BatchLimit bounds one tick's journal read; zero takes the default.
	BatchLimit int

	Logger *slog.Logger
}

// Stats are the counters `relay status` reports.
type Stats struct {
	// Published is the number of events framed and appended.
	Published int64

	// Failures is the number of publish passes that could not complete.
	Failures int64

	// LastError is the most recent failure, for status output.
	LastError string
}

// Publisher frames journal events into this peer's segment directory.
//
// It is not safe for concurrent use: two publishers appending would be
// two writers on one file, the exact shape the transport exists to
// avoid. The on-commit handler enforces that with its own guard.
type Publisher struct {
	cfg      Config
	log      *slog.Logger
	verified bool
	diverged bool
	inFlight atomic.Bool

	published int64
	failures  int64
	lastErr   string
}

// New builds a publisher, refusing a configuration that could not
// publish.
func New(cfg Config) (*Publisher, error) {
	if cfg.Journal == nil {
		return nil, errors.New("relay publisher: journal is required")
	}
	if !peerIDPattern.MatchString(cfg.PeerID) {
		return nil, fmt.Errorf("relay publisher: peer id %q is malformed", cfg.PeerID)
	}
	if cfg.Unauthenticated == (len(cfg.Key) != 0) {
		return nil, errors.New("relay publisher: exactly one of Key and Unauthenticated must be set")
	}
	if cfg.BatchLimit <= 0 {
		cfg.BatchLimit = defaultBatchLimit
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Publisher{cfg: cfg, log: log}, nil
}

// Diverged reports whether the §5.7 tail-verify refused publication.
// Ingest and reads are unaffected; only this peer's publishing halts.
func (p *Publisher) Diverged() bool { return p.diverged }

// Stats returns the counters for `relay status`.
func (p *Publisher) Stats() Stats {
	return Stats{Published: p.published, Failures: p.failures, LastError: p.lastErr}
}

// PublishPending frames every journal event past the cursor and returns
// how many were appended.
//
// The order is load-bearing: read the cursor, verify the tail once per
// run, read the journal, append, and only then advance the cursor.
func (p *Publisher) PublishPending(ctx context.Context) (int, error) {
	n, err := p.publish(ctx)
	if err != nil {
		p.failures++
		p.lastErr = err.Error()
		return n, err
	}
	p.published += int64(n)
	return n, nil
}

// publish is PublishPending without the counters.
func (p *Publisher) publish(ctx context.Context) (int, error) {
	if p.diverged {
		return 0, p.divergedError("publication is halted until this peer is reset")
	}
	pos, err := p.cfg.Journal.RelayPushCursor(ctx)
	if err != nil {
		return 0, err
	}
	if verifyErr := p.verifyTail(ctx); verifyErr != nil {
		return 0, verifyErr
	}
	rows, err := p.cfg.Journal.ReadRelayJournalSince(ctx, pos.Seq, p.cfg.BatchLimit)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	w, err := segment.NewWriter(segment.WriterConfig{
		Dir:             p.cfg.SegmentsDir,
		PeerID:          p.cfg.PeerID,
		Key:             p.cfg.Key,
		Unauthenticated: p.cfg.Unauthenticated,
		KeyEpoch:        p.cfg.KeyEpoch,
		PubEpoch:        pos.PubEpoch,
		NextRS:          pos.NextRS,
		MaxSegmentBytes: p.cfg.MaxSegmentBytes,
	})
	if err != nil {
		return 0, fmt.Errorf("open relay segment writer: %w", err)
	}
	// A writer that failed mid-publish is discarded rather than reused:
	// the next pass builds a fresh one, which runs the §5.5 tail check
	// and rotates past whatever damage was left behind.
	defer func() { _ = w.Close() }()

	if w.RecoveredFromTornTail() {
		p.log.Warn("relay publisher sealed a torn segment tail and rotated past it",
			slog.String("peer", p.cfg.PeerID), slog.Uint64("segment", w.SegmentNo()))
	}

	appended := 0
	for _, row := range rows {
		payload, marshalErr := json.Marshal(row.Event)
		if marshalErr != nil {
			return appended, fmt.Errorf("encode event %s: %w", row.Event.EventID, marshalErr)
		}
		if _, appendErr := w.Append(payload); appendErr != nil {
			// Everything appended before this point is real and on the
			// medium; bank it so the retry starts from here rather than
			// republishing what already landed.
			return appended, p.bank(ctx, rows, appended, w, appendErr)
		}
		appended++
	}
	if err := p.cfg.Journal.AdvanceRelayPushCursor(ctx, rows[len(rows)-1].Seq, w.NextRS()); err != nil {
		return appended, err
	}
	return appended, nil
}

// bank advances the cursor over the records that did land before an
// append failed, then returns the failure.
//
// Advancing here is safe for the same reason the ordinary path is: the
// bytes are already on the medium. Skipping this would republish them
// next tick, which is merely wasteful — but leaving the cursor behind a
// growing prefix makes every retry longer than the last.
func (p *Publisher) bank(ctx context.Context, rows []sqlite.RelayJournalEvent, appended int, w *segment.Writer, cause error) error {
	if appended == 0 {
		return cause
	}
	if err := p.cfg.Journal.AdvanceRelayPushCursor(ctx, rows[appended-1].Seq, w.NextRS()); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// verifyTail is the FR-21 §5.7 startup check, run once per run before
// the first publish.
//
// It asks one question: are the events this peer already published
// still in its journal, in the same order? After an ordinary crash
// between the append and the cursor advance they are — the events were
// journaled before they were framed — so republishing is absorbed and
// publication resumes. After a restore-from-backup they are not, and
// continuing would emit different events under relay sequences readers
// have already consumed, which their monotonic watermark would silently
// discard. That is the outcome this check exists to prevent, so the
// refusal is loud and names its recovery.
func (p *Publisher) verifyTail(ctx context.Context) error {
	if p.verified {
		return nil
	}
	ids, err := p.publishedTailIDs()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		p.verified = true
		return nil
	}
	seqs, err := p.cfg.Journal.LookupRelayJournalSeqs(ctx, ids)
	if err != nil {
		return err
	}

	var prev int64
	for i, id := range ids {
		seq, ok := seqs[id]
		if !ok {
			p.diverged = true
			return p.divergedError(fmt.Sprintf("published event %s is absent from the journal", id))
		}
		if i > 0 && seq <= prev {
			p.diverged = true
			return p.divergedError(fmt.Sprintf("published events are out of order in the journal at %s", id))
		}
		prev = seq
	}
	p.verified = true
	return nil
}

// publishedTailIDs returns the event ids of the last records this peer
// published, newest segment last.
func (p *Publisher) publishedTailIDs() ([]string, error) {
	segs, _, err := segment.ListSegments(p.cfg.SegmentsDir)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return nil, nil
	}
	newest := segs[len(segs)-1]
	// The newest segment is the active tail, so a torn frame at its end
	// is an append in flight rather than damage.
	res, err := segment.ScanFile(newest.Path, segment.ScanOptions{
		Key:          p.cfg.Key,
		ExpectPeerID: p.cfg.PeerID,
	})
	if err != nil {
		return nil, fmt.Errorf("verify relay tail: %w", err)
	}
	records := res.Records
	if len(records) > tailVerifyDepth {
		records = records[len(records)-tailVerifyDepth:]
	}
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		var e struct {
			EventID string `json:"event_id"`
		}
		if err := json.Unmarshal(rec.Payload, &e); err != nil || e.EventID == "" {
			return nil, fmt.Errorf("verify relay tail: record at rs %d carries no event id", rec.RS)
		}
		ids = append(ids, e.EventID)
	}
	return ids, nil
}

// divergedError renders a divergence refusal, naming its recovery. Per
// the FR-21 §9 house rule, an error that does not name the next command
// is a bug — and this one arrives through an innocent operation, so the
// operator has no other clue what to do next.
func (p *Publisher) divergedError(detail string) error {
	return fmt.Errorf("%w: %s; run `mtix sync relay reset-peer` to declare a new publisher epoch",
		ErrPublisherDiverged, detail)
}

// ResetPeer is the operator-gated recovery of FR-21 §5.7: bump the
// publisher epoch, restart relay sequences at a declared base, and
// republish from a safe floor into fresh segments.
//
// Readers accept the transition because every frame carries the epoch
// under its MAC, re-applied history dedupes, and post-restore events
// arrive at a (pub_epoch, rs) nobody has consumed — so nothing is
// silently dropped and nothing is auto-picked.
func (p *Publisher) ResetPeer(ctx context.Context, floorSeq int64, baseRS uint64) error {
	if err := p.cfg.Journal.ResetRelayPublisher(ctx, floorSeq, baseRS); err != nil {
		return err
	}
	p.diverged = false
	p.verified = true // the medium is deliberately ahead of the journal now
	p.log.Warn("relay publisher reset after a restore; republishing under a new epoch",
		slog.String("peer", p.cfg.PeerID), slog.Int64("floor", floorSeq), slog.Uint64("base_rs", baseRS))
	return nil
}

// OnCommit returns the post-mutation trigger of FR-21 §6.2 — cheap,
// best-effort, and incapable of failing the mutation that fired it.
//
// The guard is not optional. This handler runs inside the store's
// post-commit callback and its own cursor advance commits, so without
// it that write would re-enter the same handler and recurse until the
// stack gave out. A pass already in flight simply returns: the tick is
// the guarantee, and this trigger is only the fast path.
func (p *Publisher) OnCommit() func() {
	return func() {
		if !p.inFlight.CompareAndSwap(false, true) {
			return
		}
		defer p.inFlight.Store(false)
		if _, err := p.PublishPending(context.Background()); err != nil {
			p.log.Warn("relay publish failed; retrying next tick",
				slog.String("peer", p.cfg.PeerID), slog.Any("error", err))
		}
	}
}
