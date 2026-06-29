// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/jackc/pgx/v5"
)

// RestoreCollision is the structured push outcome for a RESTORE collision
// (ADR-003 §6.1, Addendum A §15) — the epoch-gated settled-vs-settled
// collision that must NOT auto-renumber (Option B).
//
// It is raised ONLY when a distinct-uid create collides with a held create
// whose epoch stamp is EARLIER than the current restore_epoch — i.e. the two
// creates straddle an operator restore-bump (a cross-epoch re-grant). A
// same-epoch race never produces one; it yields RenumberRequired (§6). The
// incoming create is BLOCKED (queued in sync_node_collisions), not inserted,
// while every other event in the push still lands (block scope, audit F-1).
//
// No node is lost (ADR-003 §9): the blocked create still lives in the pusher's
// canonical local store. Resolution is human-gated (Option B): an admin picks
// the winner via `mtix sync collisions resolve` and the loser renumbers via
// Store.RenumberSubtree (§5); no create event is ever deleted.
type RestoreCollision struct {
	// EventID is the BLOCKED incoming create_node event (not inserted on the hub).
	EventID string `json:"event_id"`
	// ProjectPrefix and DisplayPath are the contested registry key (ADR-003 §6).
	ProjectPrefix string `json:"project_prefix"`
	DisplayPath   string `json:"display_path"`
	// HeldEventID is the create that holds the number on the hub (the
	// earlier-epoch survivor that did NOT renumber).
	HeldEventID string `json:"held_event_id"`
	// HeldEpoch is the restore_epoch the held create was stamped with; it is
	// strictly less than DetectedEpoch (the cross-epoch fingerprint, §15).
	HeldEpoch int64 `json:"held_epoch"`
	// DetectedEpoch is the current restore_epoch at detection time.
	DetectedEpoch int64 `json:"detected_epoch"`
}

// OpenCollision is one unresolved RESTORE collision surfaced by
// ListOpenCollisions for `mtix sync collisions list` (ADR-003 §6.1). It
// presents BOTH contesting nodes and the available signals so an admin can
// choose which keeps the number. The older-claim hint is ADVISORY only
// (audit F-5): wall-clock timestamps are client-asserted and partly lost on
// restore, so the CLI never auto-resolves on them.
type OpenCollision struct {
	CollisionID         int64  `json:"collision_id"`
	ProjectPrefix       string `json:"project_prefix"`
	DisplayPath         string `json:"display_path"`
	HeldEventID         string `json:"held_event_id"`
	HeldUID             string `json:"held_uid"`
	HeldEpoch           int64  `json:"held_epoch"`
	HeldWallClockTS     int64  `json:"held_wall_clock_ts"`
	IncomingEventID     string `json:"incoming_event_id"`
	IncomingUID         string `json:"incoming_uid"`
	IncomingWallClockTS int64  `json:"incoming_wall_clock_ts"`
	DetectedEpoch       int64  `json:"detected_epoch"`
}

// recordCollision persists one blocked RESTORE collision into
// sync_node_collisions inside the push transaction (ADR-003 §6.1, audit F-1).
// The held side's uid and wall_clock_ts are read from the already-committed
// create row (it FKs sync_events); the incoming side comes from the blocked
// event. Idempotent: a re-push of the same blocked create hits the
// incoming_event_id unique index and is a no-op (ON CONFLICT DO NOTHING), so a
// flaky network never piles up duplicate open rows. Parameterized SQL only.
func recordCollision(ctx context.Context, tx pgx.Tx, rc *RestoreCollision,
	incomingUID string, incomingWallClockTS int64,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO sync_node_collisions
		  (project_prefix, display_path,
		   held_event_id, held_uid, held_epoch, held_wall_clock_ts,
		   incoming_event_id, incoming_uid, incoming_wall_clock_ts,
		   detected_epoch, status)
		SELECT $1, $2,
		       h.event_id, COALESCE(NULLIF(h.uid, ''), h.event_id), h.restore_epoch, h.wall_clock_ts,
		       $3, $4, $5, $6, 'open'
		  FROM sync_events h
		 WHERE h.event_id = $7
		ON CONFLICT (incoming_event_id) DO NOTHING`,
		rc.ProjectPrefix, rc.DisplayPath,
		rc.EventID, incomingUID, incomingWallClockTS,
		rc.DetectedEpoch, rc.HeldEventID,
	)
	if err != nil {
		return fmt.Errorf("record collision %s vs %s: %s",
			rc.EventID, rc.HeldEventID, redact.DSN(err.Error()))
	}
	return nil
}

// openCollisionCols is the SELECT projection shared by ListOpenCollisions and
// GetOpenCollision, kept in lockstep with scanOpenCollision so the two readers
// never drift.
const openCollisionCols = `collision_id, project_prefix, display_path,
	       held_event_id, held_uid, held_epoch, held_wall_clock_ts,
	       incoming_event_id, incoming_uid, incoming_wall_clock_ts,
	       detected_epoch`

// rowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows, so one scan
// helper serves the single-row and the iterating reader.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanOpenCollision scans one row in openCollisionCols order into c.
func scanOpenCollision(s rowScanner, c *OpenCollision) error {
	return s.Scan(
		&c.CollisionID, &c.ProjectPrefix, &c.DisplayPath,
		&c.HeldEventID, &c.HeldUID, &c.HeldEpoch, &c.HeldWallClockTS,
		&c.IncomingEventID, &c.IncomingUID, &c.IncomingWallClockTS,
		&c.DetectedEpoch,
	)
}

// ListOpenCollisions returns every unresolved RESTORE collision for a project,
// oldest-detected first (ADR-003 §6.1). It surfaces both contesting nodes and
// all available signals; the caller (`mtix sync collisions list`) presents them
// for a human decision and NEVER auto-resolves (Option B, audit F-5).
// Parameterized SQL; errors redact any DSN.
func (p *Pool) ListOpenCollisions(ctx context.Context, project string) ([]OpenCollision, error) {
	if p == nil || p.p == nil {
		return nil, fmt.Errorf("ListOpenCollisions: pool not open")
	}
	rows, err := p.p.Query(ctx, `
		SELECT `+openCollisionCols+`
		  FROM sync_node_collisions
		 WHERE status = 'open' AND project_prefix = $1
		 ORDER BY detected_at, collision_id`,
		project,
	)
	if err != nil {
		return nil, fmt.Errorf("list collisions for %s: %s", project, redact.DSN(err.Error()))
	}
	defer rows.Close()

	var out []OpenCollision
	for rows.Next() {
		var c OpenCollision
		if scanErr := scanOpenCollision(rows, &c); scanErr != nil {
			return nil, fmt.Errorf("scan collision row: %s", redact.DSN(scanErr.Error()))
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate collisions: %s", redact.DSN(err.Error()))
	}
	return out, nil
}

// GetOpenCollision returns the single open collision with the given id, or a
// zero OpenCollision (CollisionID == 0) when none is open under that id. Used
// by `mtix sync collisions resolve` to load the contest before the admin picks
// a winner (ADR-003 §6.1). Parameterized SQL; errors redact any DSN.
func (p *Pool) GetOpenCollision(ctx context.Context, collisionID int64) (OpenCollision, error) {
	if p == nil || p.p == nil {
		return OpenCollision{}, fmt.Errorf("GetOpenCollision: pool not open")
	}
	var c OpenCollision
	err := scanOpenCollision(p.p.QueryRow(ctx, `
		SELECT `+openCollisionCols+`
		  FROM sync_node_collisions
		 WHERE collision_id = $1 AND status = 'open'`,
		collisionID,
	), &c)
	switch err {
	case nil:
		return c, nil
	case pgx.ErrNoRows:
		return OpenCollision{}, nil
	default:
		return OpenCollision{}, fmt.Errorf("get collision %d: %s", collisionID, redact.DSN(err.Error()))
	}
}

// ResolveCollision records an admin's Option B decision: it flips the
// collision to status='resolved', naming the winning create and the loser's
// new display_path (ADR-003 §6.1). It is the hub-side bookkeeping AFTER the
// caller has renumbered the loser locally via Store.RenumberSubtree (§5) — no
// create event is ever deleted, so no node is lost. The older-claim default is
// advisory; the admin (not this function) chooses the winner (audit F-5).
//
// Only an OPEN row is updated (status='open' guard), so a double resolve is a
// no-op rather than a clobber; the affected row count tells the caller whether
// the decision landed. Parameterized SQL; errors redact any DSN.
func (p *Pool) ResolveCollision(ctx context.Context, collisionID int64,
	winnerEventID, loserNewPath, resolvedBy string,
) (bool, error) {
	if p == nil || p.p == nil {
		return false, fmt.Errorf("ResolveCollision: pool not open")
	}
	tag, err := p.p.Exec(ctx, `
		UPDATE sync_node_collisions
		   SET status = 'resolved',
		       winner_event_id = $2,
		       loser_new_path = $3,
		       resolved_by = $4,
		       resolved_at = now()
		 WHERE collision_id = $1 AND status = 'open'`,
		collisionID, winnerEventID, loserNewPath, resolvedBy,
	)
	if err != nil {
		return false, fmt.Errorf("resolve collision %d: %s", collisionID, redact.DSN(err.Error()))
	}
	return tag.RowsAffected() == 1, nil
}
