// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/jackc/pgx/v5"
)

// RenumberRequired is the structured outcome of a push whose incoming
// create_node duplicates an already-registered (project_prefix,
// display_path) on the hub registry (ADR-003 §6).
//
// First-writer-wins: the already-registered create keeps the number; the
// incoming create is NOT inserted and is surfaced here so the claimer can
// retry the next free number under the parent. A renumber-required
// outcome NEVER means a lost node — the rejected node still exists in the
// pusher's canonical local store; only its display number must move
// (ADR-003 §9: liveness, not a security boundary).
//
// Distinct from ConflictDescriptor: that is a field-level LWW conflict
// between concurrent updates (SYNC-DESIGN §8); this is a node-number
// collision at create time.
type RenumberRequired struct {
	// EventID is the rejected incoming create_node event.
	EventID string `json:"event_id"`
	// ProjectPrefix and DisplayPath are the contested registry key — the
	// node number the incoming create tried to claim. DisplayPath mirrors
	// SyncEvent.NodeID (the dot-notation display path, SYNC-DESIGN §3.1).
	ProjectPrefix string `json:"project_prefix"`
	DisplayPath   string `json:"display_path"`
	// RegisteredEventID is the create_node event that already holds the
	// number (the first writer that won), whether it was committed in a
	// prior push or claimed earlier in THIS push batch.
	RegisteredEventID string `json:"registered_event_id,omitempty"`
}

// lookupRegisteredCreate returns the event_id of the create_node already
// registered for (prefix, displayPath), or "" when the number is free.
// It reads the DERIVED registry — the partial unique index over the
// append-only log (ADR-003 §6, §13) — so there is no separate authoritative
// table to consult. Parameterized SQL only; errors redact any DSN.
//
// excludeEventID lets an idempotent re-push of the SAME create event see
// the number as free relative to itself: re-pushing a create must be a
// no-op, never a spurious renumber (ADR-003 §6).
func lookupRegisteredCreate(ctx context.Context, tx pgx.Tx, prefix, displayPath, excludeEventID string) (string, error) {
	var registeredID string
	err := tx.QueryRow(ctx, `
		SELECT event_id
		FROM sync_events
		WHERE project_prefix = $1
		  AND node_id = $2
		  AND op_type = 'create_node'
		  AND event_id <> $3
		LIMIT 1`,
		prefix, displayPath, excludeEventID,
	).Scan(&registeredID)
	switch err {
	case nil:
		return registeredID, nil
	case pgx.ErrNoRows:
		return "", nil
	default:
		return "", fmt.Errorf("lookup registry %s/%s: %s",
			prefix, displayPath, redact.DSN(err.Error()))
	}
}

// registryKey is the (project_prefix, display_path) tuple the registry is
// keyed on. Used to detect a second create for the same number WITHIN a
// single push batch, before any row is committed and thus before the
// partial unique index can see it.
type registryKey struct {
	prefix string
	path   string
}

// keyOf returns the registry key for a create_node event.
func keyOf(e *model.SyncEvent) registryKey {
	return registryKey{prefix: e.ProjectPrefix, path: e.NodeID}
}

// registryRenumber decides the registry outcome for a single event during
// a push (ADR-003 §6). It returns:
//   - (nil, nil) — the event is not a create_node, OR its number is free:
//     the caller proceeds to insert it (and, for a create, the number is
//     now claimed for the rest of this batch).
//   - (descriptor, nil) — the number is already taken by a DIFFERENT
//     create (first-writer-wins), so the event must renumber and is NOT
//     inserted; no node is lost (ADR-003 §9).
//
// batchClaims serializes two creates for the same number within ONE push:
// the partial unique index only sees committed rows, so the first create
// in the batch claims the key in-memory and later ones renumber against it
// (ADR-003 §6.1/F-1). It is mutated in place to record a new claim.
//
// Idempotent re-push of the SAME create event (same event_id) is excluded
// from the lookup, so it falls through as "free" and is handled as an
// ON CONFLICT DO NOTHING no-op — never a spurious renumber (ADR-003 §6).
func registryRenumber(
	ctx context.Context, tx pgx.Tx, e *model.SyncEvent, batchClaims map[registryKey]string,
) (*RenumberRequired, error) {
	if e.OpType != model.OpCreateNode {
		return nil, nil
	}
	key := keyOf(e)
	if winnerID, claimed := batchClaims[key]; claimed {
		return &RenumberRequired{
			EventID: e.EventID, ProjectPrefix: e.ProjectPrefix,
			DisplayPath: e.NodeID, RegisteredEventID: winnerID,
		}, nil
	}
	registeredID, err := lookupRegisteredCreate(ctx, tx, e.ProjectPrefix, e.NodeID, e.EventID)
	if err != nil {
		return nil, err
	}
	if registeredID != "" {
		return &RenumberRequired{
			EventID: e.EventID, ProjectPrefix: e.ProjectPrefix,
			DisplayPath: e.NodeID, RegisteredEventID: registeredID,
		}, nil
	}
	batchClaims[key] = e.EventID
	return nil, nil
}
