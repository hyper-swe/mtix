// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/hyper-swe/mtix/internal/model"
)

// inboxReturnedMetaPrefix keys the per-id returned-seq mark of an agent id that
// has no registration row (ADR-007 A12, MTIX-112.18.1).
const inboxReturnedMetaPrefix = "inbox.returned_seq."

// InboxReturnedMark returns the highest event seq already returned to agentID
// by a policy-applying inbox read (ADR-007 A12). A registered id keeps it in
// agent_registrations.returned_seq_mark; an unregistered id in a per-id meta
// key. Zero means nothing was returned yet.
func (s *Store) InboxReturnedMark(ctx context.Context, agentID string) (int64, error) {
	if agentID == "" {
		return 0, fmt.Errorf("inbox returned mark: agent id required: %w", model.ErrInvalidInput)
	}
	// Highest mark across this id's registration rows on this board; the table
	// is board-local, so one row per (host, uid) at most.
	var rows sql.NullInt64
	err := s.readDB.QueryRowContext(ctx,
		`SELECT MAX(returned_seq_mark) FROM agent_registrations WHERE agent_id = ?`, agentID).Scan(&rows)
	if err != nil {
		return 0, fmt.Errorf("read returned mark %q: %w", agentID, err)
	}
	// The per-id meta key survives registration changes (register, unregister,
	// re-register), so the mark is the larger of the two.
	var raw string
	err = s.readDB.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, inboxReturnedMetaPrefix+agentID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return rows.Int64, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read returned mark meta %q: %w", agentID, err)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("returned mark meta %q is corrupt: %w", agentID, model.ErrInvalidInput)
	}
	return max(n, rows.Int64), nil
}

// AdvanceInboxReturnedMark raises agentID's returned-seq mark to seq. It is
// monotonic: a lower seq never rewinds it (ADR-007 A12). The per-id meta key
// always records it, so a later registration change cannot reset it.
func (s *Store) AdvanceInboxReturnedMark(ctx context.Context, agentID string, seq int64) error {
	if agentID == "" || seq < 0 {
		return fmt.Errorf("advance returned mark: agent id and a non-negative seq required: %w", model.ErrInvalidInput)
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		// Raise every registration row of this id; MAX keeps it monotonic.
		if _, err := tx.ExecContext(ctx,
			`UPDATE agent_registrations SET returned_seq_mark = MAX(returned_seq_mark, ?) WHERE agent_id = ?`, seq, agentID); err != nil {
			return fmt.Errorf("advance returned mark %q: %w", agentID, err)
		}
		// Upsert the meta key, keeping the larger value.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = CAST(MAX(CAST(value AS INTEGER), CAST(excluded.value AS INTEGER)) AS TEXT)`,
			inboxReturnedMetaPrefix+agentID, strconv.FormatInt(seq, 10)); err != nil {
			return fmt.Errorf("advance returned mark meta %q: %w", agentID, err)
		}
		return nil
	})
}

// WakeChargedAfter returns the event seqs above after that the wake dispatcher
// already charged for agentID: those with a charged wake ledger row (ADR-007 A12).
func (s *Store) WakeChargedAfter(ctx context.Context, agentID string, after int64) (map[int64]bool, error) {
	if agentID == "" {
		return nil, fmt.Errorf("wake charged: agent id required: %w", model.ErrInvalidInput)
	}
	// Only rows ClaimWakeBudget wrote (charged = 1) paid the budget; a wake the
	// budget held writes none, and an unbudgeted ClaimWake row (pi wait, Claude
	// hook) charged nothing, so the read that returns its event charges it.
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT DISTINCT event_seq FROM wake_ledger WHERE agent_id = ? AND event_seq > ? AND charged = 1`, agentID, after)
	if err != nil {
		return nil, fmt.Errorf("read wake charges %q: %w", agentID, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]bool{}
	for rows.Next() {
		var seq int64
		if scanErr := rows.Scan(&seq); scanErr != nil {
			return nil, fmt.Errorf("scan wake charge: %w", scanErr)
		}
		out[seq] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wake charges %q: %w", agentID, err)
	}
	return out, nil
}
