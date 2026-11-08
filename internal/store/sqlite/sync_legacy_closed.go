// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// LegacyClosedStamps names the nodes whose stored closed_at was set by a plain,
// pre-snapshot terminal event (MTIX-112.17.16.19.1). A terminal
// transition_status event written before closed_at was carried in the payload
// has no closed_at, so apply stamps the node from the event's wall clock; the
// author's own store stamped it from a separate clock read, and the two reads
// can fall either side of a second.
//
// The evidence is about the one event that set closed_at: the node's winning
// workflow event, found by topHeldWorkflow, the function apply itself uses. A
// node is named, with its stored closed_at, only when that event is a
// transition_status to a terminal status with no closed_at and its wall-clock
// second is the stored value. A node whose winner is a modern event that carries
// closed_at, a reopen, a claim or any other op, or whose stamp was edited since,
// is absent and so is compared exactly.
func (s *Store) LegacyClosedStamps(ctx context.Context) (map[string]string, error) {
	nodes, err := s.stampedNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, n := range nodes {
		winner, found, err := s.nodeWinner(ctx, n)
		if err != nil {
			return nil, fmt.Errorf("find the event that stamped %s: %w", n.id, err)
		}
		if found && isLegacyTerminalStamp(n.closedAt, winner) {
			out[n.id] = n.closedAt
		}
	}
	return out, nil
}

type stampedNode struct{ id, uid, closedAt string }

// nodeWinner is the node's winning workflow event, matched by the node's durable
// uid as apply does for events that carry one, and by node id for events that do
// not (older events hold no uid).
func (s *Store) nodeWinner(ctx context.Context, n stampedNode) (heldWorkflow, bool, error) {
	if n.uid != "" {
		w, found, err := topHeldWorkflow(ctx, s.ReadDB(), &model.SyncEvent{NodeID: n.id, UID: n.uid})
		if err != nil || found {
			return w, found, err
		}
	}
	return topHeldWorkflow(ctx, s.ReadDB(), &model.SyncEvent{NodeID: n.id})
}

// stampedNodes lists the live nodes that hold a completion stamp.
func (s *Store) stampedNodes(ctx context.Context) ([]stampedNode, error) {
	// Live nodes holding a closed_at, with the uid that scopes their events.
	rows, err := s.ReadDB().QueryContext(ctx, `
		SELECT id, COALESCE(uid, ''), closed_at FROM nodes
		WHERE closed_at IS NOT NULL AND deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("read stamped nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []stampedNode
	for rows.Next() {
		var n stampedNode
		if err := rows.Scan(&n.id, &n.uid, &n.closedAt); err != nil {
			return nil, fmt.Errorf("scan stamped node: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stamped nodes: %w", err)
	}
	return out, nil
}

// isLegacyTerminalStamp reports whether the winning event is a terminal
// transition with no closed_at snapshot whose wall-clock second is the stamp.
func isLegacyTerminalStamp(closedAt string, w heldWorkflow) bool {
	if w.op != model.OpTransitionStatus {
		return false
	}
	wp, err := decodeWorkflowPayload(w.op, w.payload)
	if err != nil || wp.closedAt != nil || !wp.to.IsTerminal() {
		return false
	}
	return closedAtFromWallClock(w.wallMS, "") == closedAt
}
