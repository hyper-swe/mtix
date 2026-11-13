// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Relay quarantine states and resolutions (MTIX-268).
const (
	RelayQuarantineOpen     = "open"
	RelayQuarantineResolved = "resolved"
	RelayResolutionAccepted = "accepted"
	RelayResolutionRecreate = "recreated"
)

// RelayQuarantineKey names a record by its place in a peer's stream.
type RelayQuarantineKey struct {
	PeerID   string
	PubEpoch uint64
	RS       uint64
}

// RelayQuarantineRecord is a relay record that ingest skipped (FR-21 §6.4).
type RelayQuarantineRecord struct {
	RelayQuarantineKey
	SegmentNo uint64
	// EventID, NodeID, OpType and Lamport are read from the payload when it
	// decodes; they are empty or zero when it does not.
	EventID string
	NodeID  string
	OpType  string
	Lamport int64
	Reason  string
	// Payload is the exact record payload bytes as received.
	Payload []byte
	// SeenAt is the RFC 3339 UTC time of this ingest attempt.
	SeenAt string
}

// RelayQuarantineRow is a stored relay quarantine row.
type RelayQuarantineRow struct {
	RelayQuarantineRecord
	FirstSeen, LastSeen string
	Attempts            int
	State               string
	Resolution          string
	ResolvedBy          string
	ResolvedAt          string
	ResolveReason       string
	RecreatedAs         string
}

// RecordRelayQuarantine stores a skipped relay record in its own transaction
// (MTIX-268). The first sighting inserts the row with one attempt; reading the
// same record again (same peer, publisher epoch and relay sequence) only counts
// one more attempt and moves last_seen, so the first reason, payload and
// first_seen stay as first recorded, and a resolved row stays resolved.
func (s *Store) RecordRelayQuarantine(ctx context.Context, rec RelayQuarantineRecord) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		// Insert the skipped record, or count one more sighting of it.
		_, err := tx.ExecContext(ctx, `
			INSERT INTO relay_quarantine
			  (peer_id, pub_epoch, rs, segment_no, event_id, node_id, op_type, lamport,
			   reason, raw_payload, first_seen, last_seen, attempts)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
			ON CONFLICT(peer_id, pub_epoch, rs) DO UPDATE SET
			  last_seen = excluded.last_seen,
			  attempts  = relay_quarantine.attempts + 1`,
			rec.PeerID, int64(rec.PubEpoch), int64(rec.RS), int64(rec.SegmentNo), //nolint:gosec // G115: relay sequences are far below 2^63
			rec.EventID, rec.NodeID, rec.OpType, rec.Lamport,
			rec.Reason, rec.Payload, rec.SeenAt, rec.SeenAt)
		if err != nil {
			return fmt.Errorf("record relay quarantine %s rs %d: %w", rec.PeerID, rec.RS, err)
		}
		return nil
	})
}

// listRelayQuarantineSQL reads every relay quarantine row in stream order.
const listRelayQuarantineSQL = `SELECT peer_id, pub_epoch, rs, segment_no, event_id, node_id, op_type, lamport,
	reason, raw_payload, first_seen, last_seen, attempts, state, resolution, resolved_by,
	resolved_at, resolve_reason, recreated_as
	FROM relay_quarantine ORDER BY peer_id, pub_epoch, rs`

// ListRelayQuarantine returns every relay quarantine row, open and resolved, in
// (peer, publisher epoch, relay sequence) order.
func (s *Store) ListRelayQuarantine(ctx context.Context) (out []RelayQuarantineRow, err error) {
	// All relay quarantine rows; the table is small (only skipped records).
	rows, err := s.Query(ctx, listRelayQuarantineSQL)
	if err != nil {
		return nil, fmt.Errorf("list relay quarantine: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			out, err = nil, fmt.Errorf("list relay quarantine: close rows: %w", closeErr)
		}
	}()
	out = []RelayQuarantineRow{}
	for rows.Next() {
		row, scanErr := scanRelayQuarantine(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, row)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("list relay quarantine: %w", rowsErr)
	}
	return out, nil
}

func scanRelayQuarantine(scan func(...any) error) (RelayQuarantineRow, error) {
	var r RelayQuarantineRow
	var epoch, rs, seg int64
	if err := scan(&r.PeerID, &epoch, &rs, &seg, &r.EventID, &r.NodeID, &r.OpType, &r.Lamport,
		&r.Reason, &r.Payload, &r.FirstSeen, &r.LastSeen, &r.Attempts, &r.State, &r.Resolution,
		&r.ResolvedBy, &r.ResolvedAt, &r.ResolveReason, &r.RecreatedAs); err != nil {
		return r, fmt.Errorf("read relay quarantine: %w", err)
	}
	r.PubEpoch, r.RS, r.SegmentNo = uint64(epoch), uint64(rs), uint64(seg) //nolint:gosec // G115: stored from uint64 values below 2^63
	return r, nil
}

// RelayQuarantineCounts are the numbers doctor reports.
type RelayQuarantineCounts struct{ Open, Resolved int }

// CountRelayQuarantine counts open and resolved relay quarantine rows.
func (s *Store) CountRelayQuarantine(ctx context.Context) (RelayQuarantineCounts, error) {
	var c RelayQuarantineCounts
	// Open and resolved rows in one pass.
	err := s.QueryRow(ctx, `SELECT
		COALESCE(SUM(state = 'open'), 0), COALESCE(SUM(state = 'resolved'), 0)
		FROM relay_quarantine`).Scan(&c.Open, &c.Resolved)
	if err != nil {
		return c, fmt.Errorf("count relay quarantine: %w", err)
	}
	return c, nil
}

// RelayResolution is how a row was settled.
type RelayResolution struct {
	Resolution  string
	Actor       string
	Reason      string
	RecreatedAs string
	At          time.Time
}

// ResolveRelayQuarantineTx settles one open row in the caller's transaction, so
// a recreate and its mark commit together (MTIX-268). It reports false when the
// row does not exist or is already resolved; nothing is changed then.
func ResolveRelayQuarantineTx(ctx context.Context, tx *sql.Tx, key RelayQuarantineKey, res RelayResolution) (bool, error) {
	// Settle the row only while it is still open.
	r, err := tx.ExecContext(ctx, `
		UPDATE relay_quarantine
		SET state = 'resolved', resolution = ?, resolved_by = ?, resolved_at = ?,
		    resolve_reason = ?, recreated_as = ?
		WHERE peer_id = ? AND pub_epoch = ? AND rs = ? AND state = 'open'`,
		res.Resolution, res.Actor, res.At.UTC().Format(time.RFC3339), res.Reason, res.RecreatedAs,
		key.PeerID, int64(key.PubEpoch), int64(key.RS)) //nolint:gosec // G115: relay sequences are far below 2^63
	if err != nil {
		return false, fmt.Errorf("resolve relay quarantine %s rs %d: %w", key.PeerID, key.RS, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("resolve relay quarantine rows: %w", err)
	}
	return n == 1, nil
}
