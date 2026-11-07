// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// LegacyClosedStamps names the nodes whose stored closed_at was derived from a
// plain, pre-snapshot terminal event (MTIX-112.17.16.19.1). A terminal
// transition_status event written before closed_at was carried in the payload
// has no closed_at, so apply stamps the node from the event's wall clock; the
// author's own store stamped it from a separate clock read, and the two reads
// can fall either side of a second. For each node the map holds the stored
// closed_at when it equals the wall-clock second of such an event, and nothing
// otherwise: a node stamped from an event that carries closed_at, or edited
// since, is absent and so is compared exactly.
func (s *Store) LegacyClosedStamps(ctx context.Context) (map[string]string, error) {
	// Terminal transitions of live nodes that hold a completion stamp, with the
	// event wall clock the apply code stamped it from.
	rows, err := s.ReadDB().QueryContext(ctx, `
		SELECT e.node_id, n.closed_at, e.payload, e.wall_clock_ts
		FROM sync_events e
		JOIN nodes n ON n.id = e.node_id
		WHERE e.op_type = ?
		  AND n.closed_at IS NOT NULL
		  AND n.deleted_at IS NULL`, string(model.OpTransitionStatus))
	if err != nil {
		return nil, fmt.Errorf("read terminal events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, closedAt, payload string
		var wallMS int64
		if err := rows.Scan(&id, &closedAt, &payload, &wallMS); err != nil {
			return nil, fmt.Errorf("scan terminal event: %w", err)
		}
		if isLegacyTerminalStamp(closedAt, payload, wallMS) {
			out[id] = closedAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate terminal events: %w", err)
	}
	return out, nil
}

// isLegacyTerminalStamp reports whether the event is a terminal transition with
// no closed_at snapshot whose wall-clock second is the stored stamp.
func isLegacyTerminalStamp(closedAt, payload string, wallMS int64) bool {
	wp, err := decodeWorkflowPayload(model.OpTransitionStatus, []byte(payload))
	if err != nil || wp.closedAt != nil || !wp.to.IsTerminal() {
		return false
	}
	return closedAtFromWallClock(wallMS, "") == closedAt
}
