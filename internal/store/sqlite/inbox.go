// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// InboxEvent is one entry surfaced by a per-agent inbox query (FR-19.4).
// Seq is the sync_events rowid — the monotonic local sequence an agent acks.
type InboxEvent struct {
	Seq       int64     `json:"seq"`
	EventID   string    `json:"event_id"`
	NodeID    string    `json:"node_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// InboxList returns comment events addressed to agentID that are past the
// agent's ack cursor, oldest first. The inbox is DERIVED — a query over the
// durable event journal, not a separate mailbox — so it survives restarts and
// picks up synced events with no extra delivery step (FR-19.4).
func (s *Store) InboxList(ctx context.Context, agentID string) ([]InboxEvent, error) {
	if agentID == "" {
		return nil, fmt.Errorf("inbox list: agent id required: %w", model.ErrInvalidInput)
	}
	// The inbox is the UNION of two sources past the agent's ack cursor:
	// (1) comment events addressed to the agent (addressee in the payload), and
	// (2) events a hook delivered to this agent's inbox (inbox_deliveries).
	// UNION (not UNION ALL) dedupes the rare event that is both.
	rows, err := queryInboxEligible(ctx, s.readDB, agentID)
	if err != nil {
		return nil, fmt.Errorf("inbox list %q: %w", agentID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []InboxEvent
	for rows.Next() {
		var (
			ev        InboxEvent
			payload   string
			createdAt string
		)
		if scanErr := rows.Scan(&ev.Seq, &ev.EventID, &ev.NodeID, &ev.Author, &payload, &createdAt); scanErr != nil {
			return nil, fmt.Errorf("inbox scan: %w", scanErr)
		}
		var p model.CommentPayload
		if json.Unmarshal([]byte(payload), &p) == nil {
			ev.Body = p.Body
			if p.AuthorID != "" {
				ev.Author = p.AuthorID
			}
		}
		ev.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inbox iterate %q: %w", agentID, err)
	}
	s.touchFromContext(ctx, agentID) // last_seen moves only for the registered session (ADR-007 §3.3)
	return out, nil
}

// ErrSeqOutsideJournal marks an ack refused because its seq is not in this
// board's journal (FR-21 §12.4 tail clamp). It wraps model.ErrInvalidInput, so
// the exit-code class is unchanged; the CLI uses it to name the board it
// checked, because such a seq usually belongs to another board (MTIX-189).
var ErrSeqOutsideJournal = fmt.Errorf("seq outside this board's journal: %w", model.ErrInvalidInput)

// journalTail returns the current highest sync_events rowid (0 for an empty
// journal) — the upper bound of ackable inbox seqs, read inside the caller's
// transaction so the bound and the ack are consistent.
func journalTail(ctx context.Context, tx *sql.Tx) (int64, error) {
	var tail int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(rowid), 0) FROM sync_events`).Scan(&tail); err != nil {
		return 0, fmt.Errorf("inbox ack: read journal tail: %w", err)
	}
	return tail, nil
}

// InboxAck marks the SINGLE event seq as seen for agentID (MTIX-55): a
// SELECTIVE ack recorded in the per-event ledger, NOT a cumulative watermark.
// Acking a higher seq therefore never drops a lower, still-unprocessed event —
// unacked events always resurface in InboxList (at-least-once holds under
// out-of-order processing). Idempotent per (agent, seq). To defer an event,
// simply do not ack it; it reappears on the next InboxList.
//
// The seq must reference the existing journal (1..tail): acking a seq that
// does not exist yet would pre-acknowledge a FUTURE event — a message the
// agent has never seen would arrive already-seen and be silently dropped
// from the inbox (relay-epic prerequisite fix; see FR-21 §12.4).
func (s *Store) InboxAck(ctx context.Context, agentID string, seq int64) error {
	_, _, err := s.InboxAckCoverage(ctx, agentID, seq, false)
	return err
}

// InboxAckThrough is the explicit CUMULATIVE ack for a caller that DID process
// in order: it advances agentID's watermark so every event with rowid <= seq is
// marked seen, and prunes now-redundant ledger rows at/below the watermark to
// keep the ledger bounded. Monotonic; a lower seq never rewinds the watermark.
//
// A seq outside the existing journal (seq < 1 or seq > tail) is REJECTED —
// not clamped, not partially applied: an accepted out-of-journal cursor would
// pre-acknowledge every future event up to seq, so the agent's inbox would
// silently swallow messages it never saw (relay-epic prerequisite fix; see
// FR-21 §12.4). The caller acks what exists, or the call fails loudly.
func (s *Store) InboxAckThrough(ctx context.Context, agentID string, seq int64) error {
	_, _, err := s.InboxAckCoverage(ctx, agentID, seq, true)
	return err
}

// inboxPollInterval is how often InboxWait re-queries while blocked. Small
// enough to feel instant to a parked worker, large enough not to spin the DB.
const inboxPollInterval = 250 * time.Millisecond

// InboxWait first reads pending events using the caller context, then polls
// without holding a transaction between queries (FR-19.4). The initial read
// remains caller-interruptible even after the local timeout expires. A positive
// timeout is measured from entry and bounds only the subsequent wait for future
// events; expiration returns (nil, nil). Zero waits until an event or caller
// cancellation. Parent cancellation, deadlines and database errors propagate.
// Negative durations return ErrInvalidInput before reading the inbox.
func (s *Store) InboxWait(ctx context.Context, agentID string, timeout time.Duration) ([]InboxEvent, error) {
	if timeout < 0 {
		return nil, fmt.Errorf("inbox wait: negative timeout: %w", model.ErrInvalidInput)
	}
	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	ticker := time.NewTicker(inboxPollInterval)
	defer ticker.Stop()
	queryCtx := ctx
	for {
		events, err := s.InboxList(queryCtx, agentID)
		if err != nil {
			return nil, inboxWaitError(ctx, queryCtx, err)
		}
		queryCtx = waitCtx
		if len(events) > 0 {
			return events, nil
		}
		select {
		case <-waitCtx.Done():
			return nil, inboxWaitError(ctx, waitCtx, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// Only the wait's own elapsed timeout is an empty success; cancellation from
// its caller remains an error. Timers use scheduler time, so a frozen injected
// timestamp clock never prevents a finite wait from expiring.
func inboxWaitError(parent, waiter context.Context, err error) error {
	if parent.Err() != nil {
		return fmt.Errorf("inbox wait interrupted: %w", parent.Err())
	}
	if errors.Is(err, context.DeadlineExceeded) && waiter.Err() == context.DeadlineExceeded {
		return nil
	}
	return fmt.Errorf("inbox wait: %w", err)
}
