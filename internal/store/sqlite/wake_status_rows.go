// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// maxWakeStatusRows bounds one status listing.
const maxWakeStatusRows = 1000

// WakeStatusRows lists the newest retained wake ledger rows of one agent on one host and
// user, for sender-visible delivery status (MTIX-112.40.6). It is read-only: it never
// claims, prunes or writes, and other hosts' or users' rows are not ours to show (I31).
func (s *Store) WakeStatusRows(ctx context.Context, hostID, uid, agent string, limit int) ([]model.WakeStatusRow, error) {
	if hostID == "" || uid == "" || agent == "" || limit < 1 || limit > maxWakeStatusRows {
		return nil, fmt.Errorf("list wake status: host, uid, agent and 1..%d limit are required: %w", maxWakeStatusRows, model.ErrInvalidInput)
	}
	// Newest events first; the ledger key scopes the rows to this agent, host and uid.
	rows, err := s.readDB.QueryContext(ctx, `SELECT event_seq, outcome, route, reason, claimed_at, outcome_at
		FROM wake_ledger WHERE agent_id=? AND host_id=? AND uid=?
		ORDER BY event_seq DESC, epoch DESC LIMIT ?`, agent, hostID, uid, limit)
	if err != nil {
		return nil, fmt.Errorf("list wake status: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []model.WakeStatusRow
	for rows.Next() {
		var r model.WakeStatusRow
		if err := rows.Scan(&r.EventSeq, &r.Outcome, &r.Route, &r.Reason, &r.ClaimedAt, &r.OutcomeAt); err != nil {
			return nil, fmt.Errorf("scan wake status: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read wake status: %w", err)
	}
	return out, nil
}
