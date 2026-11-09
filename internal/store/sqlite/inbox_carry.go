// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
)

// ErrCarryEventMissing marks a carried hook delivery whose event the target
// store does not hold: carrying it would drop an unread message (MTIX-112.17.16).
var ErrCarryEventMissing = errors.New("carried inbox delivery names an event the store does not hold")

// CarriedAck is one acknowledgement keyed by the event id, never by seq: seqs
// are local insert order and differ between a store and its hub clone.
type CarriedAck struct {
	AgentID string
	EventID string
	// AckedAt is zero when the ack was a watermark ack with no recorded time.
	AckedAt time.Time
}

// CarriedDelivery is one hook delivery keyed by the event id.
type CarriedDelivery struct {
	AgentID     string
	EventID     string
	HookName    string
	DeliveredAt string
}

// InboxCarry is the inbox state that moves from a store to its hub clone.
type InboxCarry struct {
	Acks       []CarriedAck
	Deliveries []CarriedDelivery
}

// InboxCarryResult says what ImportInboxCarry wrote.
type InboxCarryResult struct {
	Acks, Deliveries, UnmatchedAcks int
}

// carryAgentsSQL lists every agent with inbox state or mail in the store.
const carryAgentsSQL = `SELECT agent_id FROM agent_inbox_ack
UNION SELECT agent_id FROM agent_inbox_cursor
UNION SELECT agent_id FROM inbox_deliveries
UNION SELECT json_extract(payload, '$.to') FROM sync_events
 WHERE op_type = 'comment' AND json_valid(payload)`

// carryLedgerSQL lists the individually acknowledged (agent, event) pairs,
// including ledger rows the history query would not show as addressed.
const carryLedgerSQL = `SELECT k.agent_id, e.event_id, k.acked_at
FROM agent_inbox_ack k JOIN sync_events e ON e.rowid = k.event_seq`

// carryDeliveriesSQL lists the hook deliveries with their event ids.
const carryDeliveriesSQL = `SELECT d.agent_id, e.event_id, d.hook_name, d.delivered_at
FROM inbox_deliveries d JOIN sync_events e ON e.rowid = d.event_seq`

// ExportInboxCarry reads what a hub clone must keep of this store's inbox: the
// acknowledged (agent, event id) pairs, from the per-event ledger and from the
// events at or below an agent's watermark, and the hook deliveries. An event the
// agent did not acknowledge is never listed (MTIX-112.17.16). It refuses, as an
// explicit export does, when the acknowledgment index disagrees with its table
// (MTIX-112.49). The check and every read share one read-only WAL snapshot, as
// ExportWithInbox does, so a write between them cannot go unchecked. Read-only.
func (s *Store) ExportInboxCarry(ctx context.Context) (carry *InboxCarry, err error) {
	// The reader pool begins deferred; a WAL snapshot never takes writer ownership.
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin inbox carry snapshot: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			carry = nil
			err = errors.Join(err, fmt.Errorf("rollback inbox carry snapshot: %w", rollbackErr))
		}
	}()
	if err := checkAckIndexAgreesWithTable(ctx, tx); err != nil {
		return nil, err
	}
	out, err := readInboxCarry(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit inbox carry snapshot: %w", err)
	}
	return out, nil
}

// readInboxCarry reads the ledger acks, watermark acks and hook deliveries
// through q, which is the carry's snapshot transaction.
func readInboxCarry(ctx context.Context, q inboxQuerier) (*InboxCarry, error) {
	carry := &InboxCarry{}
	seen := map[[2]string]bool{}
	if err := collectLedgerAcks(ctx, q, carry, seen); err != nil {
		return nil, err
	}
	agents, err := carryAgents(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, agent := range agents {
		if err := collectWatermarkAcks(ctx, q, agent, carry, seen); err != nil {
			return nil, err
		}
	}
	if err := collectDeliveries(ctx, q, carry); err != nil {
		return nil, err
	}
	return carry, nil
}

func carryAgents(ctx context.Context, q inboxQuerier) ([]string, error) {
	rows, err := q.QueryContext(ctx, carryAgentsSQL)
	if err != nil {
		return nil, fmt.Errorf("list inbox agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var a sql.NullString
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("scan inbox agent: %w", err)
		}
		if a.Valid && a.String != "" {
			out = append(out, a.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox agents: %w", err)
	}
	return out, nil
}

func collectLedgerAcks(ctx context.Context, q inboxQuerier, carry *InboxCarry, seen map[[2]string]bool) error {
	rows, err := q.QueryContext(ctx, carryLedgerSQL)
	if err != nil {
		return fmt.Errorf("read inbox ack ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var agent, event, at string
		if scanErr := rows.Scan(&agent, &event, &at); scanErr != nil {
			return fmt.Errorf("scan inbox ack ledger: %w", scanErr)
		}
		var t time.Time
		if at != "" {
			if t, err = time.Parse(time.RFC3339Nano, at); err != nil {
				return fmt.Errorf("parse acked_at %q of an ack on event %s: %w", at, format.EventID(event), err)
			}
		}
		carry.Acks = append(carry.Acks, CarriedAck{AgentID: agent, EventID: event, AckedAt: t})
		seen[[2]string{agent, event}] = true
	}
	return rows.Err()
}

func collectWatermarkAcks(ctx context.Context, q inboxQuerier, agent string, carry *InboxCarry, seen map[[2]string]bool) error {
	hist, err := inboxHistory(ctx, q, agent)
	if err != nil {
		return err
	}
	for _, h := range hist {
		key := [2]string{agent, h.EventID}
		if h.State != InboxAckedThrough || seen[key] {
			continue
		}
		carry.Acks = append(carry.Acks, CarriedAck{AgentID: agent, EventID: h.EventID})
		seen[key] = true
	}
	return nil
}

func collectDeliveries(ctx context.Context, q inboxQuerier, carry *InboxCarry) error {
	rows, err := q.QueryContext(ctx, carryDeliveriesSQL)
	if err != nil {
		return fmt.Errorf("read inbox deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var d CarriedDelivery
		if err := rows.Scan(&d.AgentID, &d.EventID, &d.HookName, &d.DeliveredAt); err != nil {
			return fmt.Errorf("scan inbox delivery: %w", err)
		}
		carry.Deliveries = append(carry.Deliveries, d)
	}
	return rows.Err()
}

// ImportInboxCarry writes a carry into this store in one transaction, mapping
// each event id to this store's own seq. An ack whose event this store does not
// hold is counted, not invented. A delivery whose event is missing refuses the
// whole import (ErrCarryEventMissing): the message would be lost. at stamps an
// ack that carried no time.
func (s *Store) ImportInboxCarry(ctx context.Context, carry *InboxCarry, at time.Time) (InboxCarryResult, error) {
	var res InboxCarryResult
	if carry == nil {
		return res, nil
	}
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		res = InboxCarryResult{}
		for _, a := range carry.Acks {
			if err := importCarriedAck(ctx, tx, a, at, &res); err != nil {
				return err
			}
		}
		for _, d := range carry.Deliveries {
			if err := importCarriedDelivery(ctx, tx, d, &res); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return InboxCarryResult{}, fmt.Errorf("import inbox carry: %w", err)
	}
	return res, nil
}

// eventSeq returns this store's seq for an event id; ok is false when absent.
func eventSeq(ctx context.Context, tx *sql.Tx, eventID string) (seq int64, ok bool, err error) {
	// Resolve the event id to this store's own insert-order seq.
	err = tx.QueryRowContext(ctx, `SELECT rowid FROM sync_events WHERE event_id = ?`, eventID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("look up event %s: %w", format.EventID(eventID), err)
	}
	return seq, true, nil
}

func importCarriedAck(ctx context.Context, tx *sql.Tx, a CarriedAck, at time.Time, res *InboxCarryResult) error {
	seq, ok, err := eventSeq(ctx, tx, a.EventID)
	if err != nil {
		return err
	}
	if !ok {
		res.UnmatchedAcks++
		return nil
	}
	when := a.AckedAt
	if when.IsZero() {
		when = at
	}
	// Record the per-event ack under this store's seq; an existing row wins.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_inbox_ack(agent_id,event_seq,acked_at) VALUES(?,?,?)`,
		a.AgentID, seq, when.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("write carried ack: %w", err)
	}
	res.Acks++
	return nil
}

func importCarriedDelivery(ctx context.Context, tx *sql.Tx, d CarriedDelivery, res *InboxCarryResult) error {
	seq, ok, err := eventSeq(ctx, tx, d.EventID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("event %s for agent %s: %w", format.EventID(d.EventID), d.AgentID, ErrCarryEventMissing)
	}
	// Record the delivery under this store's seq so the message stays in the inbox.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO inbox_deliveries(agent_id,event_seq,hook_name,delivered_at) VALUES(?,?,?,?)`,
		d.AgentID, seq, d.HookName, d.DeliveredAt); err != nil {
		return fmt.Errorf("write carried delivery: %w", err)
	}
	res.Deliveries++
	return nil
}
