// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// checkResolveTarget refuses, as invalid input, a resolve of a conflict
// with a later manual or lww row for its node and field: its decision
// would also close every conflict recorded after it, unreviewed. The error
// names the newest conflict of the node and field: the one to resolve,
// or, when it is resolved too, the one whose decision already stands. It
// runs on the resolve's own transaction (MTIX-95.7).
//
// Self-replay rows (MTIX-216, see readConflicts) take no part: a later
// self-replay row is neither a later conflict nor the newest one, so it can
// neither block the resolve of a genuine conflict nor send the user to
// resolve it instead. A self-replay row is not a conflict, so resolving one
// is refused: its decision would close earlier genuine conflicts of the
// node and field unreviewed.
func checkResolveTarget(ctx context.Context, tx *sql.Tx, target ConflictRow) error {
	pairRows, err := readPairRows(ctx, tx, target)
	if err != nil {
		return fmt.Errorf("mtix sync conflicts resolve: read later conflicts: %w", err)
	}
	for _, r := range pairRows {
		if r.SelfReplay && r.ConflictID == target.ConflictID {
			return fmt.Errorf("mtix sync conflicts resolve: conflict_id %d is a self-replay row "+
				"(both events were emitted by this store), not a conflict; nothing to resolve: %w",
				target.ConflictID, model.ErrInvalidInput)
		}
	}
	later, newest, newestResolved := laterDecisions(pairRows, target.ConflictID)
	if !later {
		return nil
	}
	pair := "node " + target.NodeID + ", " + fieldLabel(target.FieldName)
	if newest > target.ConflictID && !newestResolved {
		return fmt.Errorf("mtix sync conflicts resolve: conflict_id %d has a later conflict on %s; "+
			"resolve conflict_id %d instead, the newest conflict for that node and field: %w",
			target.ConflictID, pair, newest, model.ErrInvalidInput)
	}
	return fmt.Errorf("mtix sync conflicts resolve: conflict_id %d is already resolved: a later decision "+
		"covers %s, whose newest conflict is conflict_id %d, also resolved: %w",
		target.ConflictID, pair, newest, model.ErrInvalidInput)
}

// pairRow is one sync_conflicts row of a node and field, with whether it
// is a self-replay row.
type pairRow struct {
	ConflictID int64
	Resolution string
	SelfReplay bool
}

// laterDecisions reads a node and field's rows, ignoring self-replay rows:
// whether a manual or lww row follows target, the newest lww conflict
// (target itself when none is newer), and whether a manual row follows
// that newest conflict.
func laterDecisions(rows []pairRow, target int64) (later bool, newest int64, newestResolved bool) {
	newest = target
	for _, r := range rows {
		if r.SelfReplay {
			continue
		}
		if r.ConflictID > target && (r.Resolution == "manual" || r.Resolution == "lww") {
			later = true
		}
		if r.Resolution == "lww" && r.ConflictID > newest {
			newest = r.ConflictID
		}
	}
	for _, r := range rows {
		if r.Resolution == "manual" && r.ConflictID > newest {
			newestResolved = true
		}
	}
	return later, newest, newestResolved
}

// readPairRows returns every sync_conflicts row for target's node and
// field (NULL fields pair with each other) in conflict_id order, each
// marked self-replay by the rule readConflicts documents.
func readPairRows(ctx context.Context, tx *sql.Tx, target ConflictRow) ([]pairRow, error) {
	// Rows of one node and field with the self-replay flag: an lww row whose
	// winner and loser are both in sync_events with a status other than
	// applied (emitted by this store). Mirrors readConflicts.
	rows, err := tx.QueryContext(ctx, `
		SELECT sc.conflict_id, sc.resolution,
		       (sc.resolution = 'lww'
		        AND EXISTS (SELECT 1 FROM sync_events w
		                    WHERE w.event_id = sc.event_id_winner AND w.sync_status <> 'applied')
		        AND EXISTS (SELECT 1 FROM sync_events l
		                    WHERE l.event_id = sc.event_id_loser AND l.sync_status <> 'applied')
		       ) AS self_replay
		FROM sync_conflicts sc
		WHERE sc.node_id = ?1 AND sc.field_name IS ?2
		ORDER BY sc.conflict_id`, target.NodeID, nullIfEmpty(target.FieldName))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []pairRow
	for rows.Next() {
		var r pairRow
		if err := rows.Scan(&r.ConflictID, &r.Resolution, &r.SelfReplay); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
