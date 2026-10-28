// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// persistConflicts writes each detected field-level conflict to the
// hub's sync_conflicts table inside the push transaction (MTIX-15.5.2).
// Resolution is recorded as 'lww'; a manual override via mtix sync
// conflicts resolve (15.7) INSERTs a new resolution='manual' row that
// supersedes the lww row, because the 006_triggers.sql trigger forbids
// UPDATE on sync_conflicts. Parameterized SQL only.
func persistConflicts(ctx context.Context, tx pgx.Tx, conf []ConflictDescriptor) error {
	for _, c := range conf {
		if _, err := tx.Exec(ctx, `
			INSERT INTO sync_conflicts
			  (event_id_a, event_id_b, node_id, field_name, resolution)
			VALUES ($1, $2, $3, $4, 'lww')`,
			c.NewEventID, c.ConflictingEventID, c.NodeID, c.FieldName,
		); err != nil {
			return fmt.Errorf("persist conflict %s vs %s: %w",
				format.EventID(c.NewEventID), format.EventID(c.ConflictingEventID), err)
		}
	}
	return nil
}

// detectConflicts looks for prior events on the same node that are
// concurrent with e per VectorClock.Concurrent. Returns descriptors
// for every concurrent prior; resolution is a 15.5 concern.
//
// Scoping: only field-level updates (update_field, set_acceptance,
// set_prompt) are checked, since other op_types (claim, transition,
// delete, link_dep, etc.) have natural single-row outcomes that don't
// produce field-level conflicts.
func detectConflicts(ctx context.Context, tx pgx.Tx, e *model.SyncEvent) ([]ConflictDescriptor, error) {
	switch e.OpType {
	case model.OpUpdateField, model.OpSetAcceptance, model.OpSetPrompt:
	default:
		return nil, nil
	}
	fieldName, _ := extractFieldName(e)

	rows, err := tx.Query(ctx, `
		SELECT event_id, vector_clock
		FROM sync_events
		WHERE node_id = $1
		  AND op_type IN ('update_field','set_acceptance','set_prompt')
		  AND event_id <> $2`,
		e.NodeID, e.EventID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConflictDescriptor
	for rows.Next() {
		var priorID, priorVCJSON string
		if err := rows.Scan(&priorID, &priorVCJSON); err != nil {
			return nil, err
		}
		var priorVC model.VectorClock
		if err := json.Unmarshal([]byte(priorVCJSON), &priorVC); err != nil {
			continue // skip un-parseable; not a conflict by definition
		}
		if e.VectorClock.Concurrent(priorVC) {
			out = append(out, ConflictDescriptor{
				NewEventID:         e.EventID,
				ConflictingEventID: priorID,
				NodeID:             e.NodeID,
				FieldName:          fieldName,
			})
		}
	}
	return out, rows.Err()
}

// extractFieldName pulls the field_name from update_field payloads
// for conflict-descriptor decoration. Returns "" for set_acceptance /
// set_prompt (the op_type is the field).
func extractFieldName(e *model.SyncEvent) (string, error) {
	if e.OpType != model.OpUpdateField {
		return "", nil
	}
	var p model.UpdateFieldPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return "", err
	}
	return p.FieldName, nil
}
