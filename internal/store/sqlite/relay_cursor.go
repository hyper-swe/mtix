// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/hyper-swe/mtix/internal/model"
)

// relayPeerIDPattern is the FR-21 §5.2 identity grammar. The store
// validates it at this boundary so a malformed id can never occupy a
// cursor row and shadow a real peer's position.
var relayPeerIDPattern = regexp.MustCompile(`^[0-9a-f]{16}(-[a-z0-9_-]{1,32})?$`)

// RelayPushPosition is this peer's durable publish position (FR-21 §6.2).
type RelayPushPosition struct {
	// Seq is the highest sync_events.rowid already framed and appended.
	Seq int64

	// PubEpoch is the publisher restore epoch carried in every frame.
	PubEpoch uint16

	// NextRS is the relay sequence the next published record carries.
	NextRS uint64
}

// RelayIngestPosition is a reader's durable position in one peer's
// stream (FR-21 §6.3).
type RelayIngestPosition struct {
	SegmentNo uint64
	RS        uint64
	PubEpoch  uint16
}

// RelayJournalEvent is one journal row with the local sequence the relay
// publish cursor is measured in.
type RelayJournalEvent struct {
	// Seq is sync_events.rowid — a monotonic LOCAL insert order, which
	// is why it is the cursor rather than a lamport clock: a
	// late-arriving synced event always sorts above the cursor and is
	// never silently skipped.
	Seq int64

	// Event is the row itself, as it will be framed into a segment.
	Event model.SyncEvent
}

// relayEpoch and relayUnsigned convert a stored column to the frame's
// own field widths, refusing anything outside them.
//
// These tables are local meta, but a value read back that cannot appear
// in a frame means the row is damaged — and turning a negative int64
// into a vast unsigned position would hand the publisher a cursor far
// past the journal, silently skipping every event behind it. A refusal
// is recoverable; a skip is not.
func relayEpoch(column string, v int64) (uint16, error) {
	if v < 0 || v > math.MaxUint16 {
		return 0, fmt.Errorf("relay cursor: %s is %d, outside the frame's range: %w",
			column, v, model.ErrInvalidInput)
	}
	return uint16(v), nil
}

func relayUnsigned(column string, v int64) (uint64, error) {
	if v < 0 {
		return 0, fmt.Errorf("relay cursor: %s is negative (%d): %w", column, v, model.ErrInvalidInput)
	}
	return uint64(v), nil
}

// RelayPushCursor returns this peer's publish position. A store that has
// never published reports sequence zero at epoch 1, relay sequence 1.
func (s *Store) RelayPushCursor(ctx context.Context) (RelayPushPosition, error) {
	pos := RelayPushPosition{PubEpoch: 1, NextRS: 1}
	row := s.readDB.QueryRowContext(ctx,
		`SELECT cursor, pub_epoch, next_rs FROM relay_push_cursor WHERE id = 1`)
	var epoch, nextRS int64
	err := row.Scan(&pos.Seq, &epoch, &nextRS)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return pos, nil
	case err != nil:
		return RelayPushPosition{}, fmt.Errorf("read relay push cursor: %w", err)
	}
	if pos.PubEpoch, err = relayEpoch("pub_epoch", epoch); err != nil {
		return RelayPushPosition{}, err
	}
	if pos.NextRS, err = relayUnsigned("next_rs", nextRS); err != nil {
		return RelayPushPosition{}, err
	}
	return pos, nil
}

// AdvanceRelayPushCursor records that everything through seq has been
// appended, and that the next record carries nextRS.
//
// The advance is monotonic in seq: a lower value is absorbed rather than
// applied, so a retry or an out-of-order tick can never re-expose events
// that were already published. Moving the cursor backwards is reserved
// for ResetRelayPublisher, which may only do it while bumping the epoch.
func (s *Store) AdvanceRelayPushCursor(ctx context.Context, seq int64, nextRS uint64) error {
	// #nosec G115 -- nextRS is a relay sequence bounded by the journal.
	_, err := s.writeDB.ExecContext(ctx, `
		INSERT INTO relay_push_cursor (id, cursor, next_rs) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			cursor  = MAX(cursor, excluded.cursor),
			next_rs = CASE WHEN excluded.cursor > cursor THEN excluded.next_rs ELSE next_rs END`,
		seq, int64(nextRS))
	if err != nil {
		return fmt.Errorf("advance relay push cursor: %w", err)
	}
	return nil
}

// ResetRelayPublisher is the operator-gated recovery of FR-21 §5.7.
//
// It bumps the publisher epoch, restarts relay sequences at a declared
// base, and rewinds the publish cursor to a safe floor so history is
// republished. This is the only path allowed to move the cursor
// backwards, and it may not do so without the epoch bump: post-restore
// events must carry a (pub_epoch, rs) no reader has consumed, or the
// readers' monotonic watermark — correct against attackers — would
// silently discard the restored peer's new work.
//
// The epoch always moves forward, so two restores can never collide on
// one epoch and place unrelated histories at the same position.
func (s *Store) ResetRelayPublisher(ctx context.Context, floorSeq int64, baseRS uint64) error {
	if baseRS == 0 {
		return fmt.Errorf("reset relay publisher: base relay sequence must be at least 1: %w",
			model.ErrInvalidInput)
	}
	if floorSeq < 0 {
		return fmt.Errorf("reset relay publisher: floor must not be negative: %w", model.ErrInvalidInput)
	}
	// #nosec G115 -- baseRS is operator-declared and bounded by the frame.
	_, err := s.writeDB.ExecContext(ctx, `
		INSERT INTO relay_push_cursor (id, cursor, pub_epoch, next_rs) VALUES (1, ?, 2, ?)
		ON CONFLICT(id) DO UPDATE SET
			cursor    = excluded.cursor,
			pub_epoch = pub_epoch + 1,
			next_rs   = excluded.next_rs`,
		floorSeq, int64(baseRS))
	if err != nil {
		return fmt.Errorf("reset relay publisher: %w", err)
	}
	return nil
}

// RelayIngestCursor returns the reader's position in one peer's stream.
// A peer never read from reports the zero position.
func (s *Store) RelayIngestCursor(ctx context.Context, peerID string) (RelayIngestPosition, error) {
	if !relayPeerIDPattern.MatchString(peerID) {
		return RelayIngestPosition{}, fmt.Errorf("relay ingest cursor: peer id %q is malformed: %w",
			peerID, model.ErrInvalidInput)
	}
	row := s.readDB.QueryRowContext(ctx,
		`SELECT segment_no, rs, pub_epoch FROM relay_ingest_cursor WHERE peer_id = ?`, peerID)
	var segNo, rs, epoch int64
	err := row.Scan(&segNo, &rs, &epoch)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return RelayIngestPosition{}, nil
	case err != nil:
		return RelayIngestPosition{}, fmt.Errorf("read relay ingest cursor: %w", err)
	}
	var pos RelayIngestPosition
	var convErr error
	if pos.SegmentNo, convErr = relayUnsigned("segment_no", segNo); convErr != nil {
		return RelayIngestPosition{}, convErr
	}
	if pos.RS, convErr = relayUnsigned("rs", rs); convErr != nil {
		return RelayIngestPosition{}, convErr
	}
	if pos.PubEpoch, convErr = relayEpoch("pub_epoch", epoch); convErr != nil {
		return RelayIngestPosition{}, convErr
	}
	return pos, nil
}

// AdvanceRelayIngestCursor records how far a peer's stream has been
// applied. Callers advance it only after the applying transaction
// commits, and never past a segment they condemned.
//
// Ordering is keyed on (pub_epoch, rs), never rs alone. Within an epoch
// the position is a monotonic watermark, so a lower one is a replay and
// is absorbed. A higher epoch restarts sequences at a declared base
// (§5.7), so a lower rs under a newer epoch is legitimate progress and
// is taken. An older epoch is a rollback splice and is refused however
// high its rs looks.
func (s *Store) AdvanceRelayIngestCursor(ctx context.Context, peerID string, pos RelayIngestPosition) error {
	if !relayPeerIDPattern.MatchString(peerID) {
		return fmt.Errorf("advance relay ingest cursor: peer id %q is malformed: %w",
			peerID, model.ErrInvalidInput)
	}
	// #nosec G115 -- positions are bounded by the frame's own field widths.
	_, err := s.writeDB.ExecContext(ctx, `
		INSERT INTO relay_ingest_cursor (peer_id, segment_no, rs, pub_epoch) VALUES (?, ?, ?, ?)
		ON CONFLICT(peer_id) DO UPDATE SET
			segment_no = CASE WHEN excluded.pub_epoch > pub_epoch
			                    OR (excluded.pub_epoch = pub_epoch AND excluded.rs > rs)
			                  THEN excluded.segment_no ELSE segment_no END,
			rs         = CASE WHEN excluded.pub_epoch > pub_epoch
			                    OR (excluded.pub_epoch = pub_epoch AND excluded.rs > rs)
			                  THEN excluded.rs ELSE rs END,
			pub_epoch  = MAX(pub_epoch, excluded.pub_epoch)`,
		peerID, int64(pos.SegmentNo), int64(pos.RS), int64(pos.PubEpoch))
	if err != nil {
		return fmt.Errorf("advance relay ingest cursor: %w", err)
	}
	return nil
}

// ReadRelayJournalSince returns journal rows past seq, in local insert
// order, ready to be framed.
//
// It reads the whole event rather than the hook journal's summary
// because a relay record carries the canonical event verbatim — the
// reader on the far side runs the same validation envelope a hub would,
// and it can only do that with every field present.
func (s *Store) ReadRelayJournalSince(ctx context.Context, seq int64, limit int) ([]RelayJournalEvent, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT rowid, event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash, uid
		FROM sync_events
		WHERE rowid > ?
		ORDER BY rowid ASC
		LIMIT ?`, seq, limit)
	if err != nil {
		return nil, fmt.Errorf("read relay journal: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]RelayJournalEvent, 0, limit)
	for rows.Next() {
		var rec RelayJournalEvent
		var opType, payload, vc string
		var uid sql.NullString
		if scanErr := rows.Scan(
			&rec.Seq, &rec.Event.EventID, &rec.Event.ProjectPrefix, &rec.Event.NodeID,
			&opType, &payload, &rec.Event.WallClockTS, &rec.Event.LamportClock, &vc,
			&rec.Event.AuthorID, &rec.Event.AuthorMachineHash, &uid,
		); scanErr != nil {
			return nil, fmt.Errorf("scan relay journal row: %w", scanErr)
		}
		rec.Event.OpType = model.OpType(opType)
		rec.Event.Payload = json.RawMessage(payload)
		rec.Event.UID = uid.String
		if err := json.Unmarshal([]byte(vc), &rec.Event.VectorClock); err != nil {
			return nil, fmt.Errorf("decode vector clock for %s: %w", rec.Event.EventID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read relay journal: %w", err)
	}
	return out, nil
}

// LookupRelayJournalSeqs maps event ids to their local journal sequence,
// omitting ids the journal does not hold.
//
// The FR-21 §5.7 startup tail-verify uses it to ask the one question
// that separates an ordinary crash from a restore: are the events this
// peer already published still in its journal, in the same order? After
// a crash between the append and the cursor advance they are — the
// events were journaled before they were framed — and republishing them
// is absorbed downstream. After a restore-from-backup they are not, and
// continuing to publish would emit different events under relay
// sequences readers have already consumed.
func (s *Store) LookupRelayJournalSeqs(ctx context.Context, eventIDs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(eventIDs))
	if len(eventIDs) == 0 {
		return out, nil
	}
	placeholders := make([]byte, 0, len(eventIDs)*2)
	args := make([]any, 0, len(eventIDs))
	for i, id := range eventIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, id)
	}
	// #nosec G202 -- the interpolated text is a generated placeholder
	// list; every value is bound, per NFR-5.8.
	query := `SELECT event_id, rowid FROM sync_events WHERE event_id IN (` + string(placeholders) + `)`
	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("look up relay journal sequences: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id string
		var seq int64
		if scanErr := rows.Scan(&id, &seq); scanErr != nil {
			return nil, fmt.Errorf("scan relay journal sequence: %w", scanErr)
		}
		out[id] = seq
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("look up relay journal sequences: %w", err)
	}
	return out, nil
}

// RepublishRelayFrom rewinds the publish cursor to a floor so the
// events after it are framed again, keeping the publisher epoch and the
// relay sequence where they are (FR-21 §6.8).
//
// This is gap repair, not restore, and the difference is the whole
// reason it is a separate operation from ResetRelayPublisher. The
// events being re-emitted are the SAME events; they simply need to
// reach a reader that lost them to a condemned segment. So they go out
// under fresh relay sequences in fresh segments — never reusing
// positions, never touching a file that already exists — and the
// duplicates are absorbed by applied_events. Bumping the epoch here
// would tell readers a restore had happened when none had.
func (s *Store) RepublishRelayFrom(ctx context.Context, floorSeq int64) error {
	if floorSeq < 0 {
		return fmt.Errorf("republish relay: floor must not be negative: %w", model.ErrInvalidInput)
	}
	res, err := s.writeDB.ExecContext(ctx,
		`UPDATE relay_push_cursor SET cursor = ? WHERE id = 1`, floorSeq)
	if err != nil {
		return fmt.Errorf("republish relay: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("republish relay: %w", err)
	}
	if n == 0 {
		// Nothing has ever been published, so everything in the journal
		// is already ahead of the cursor and there is nothing to repair.
		return nil
	}
	return nil
}
