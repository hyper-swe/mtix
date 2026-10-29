// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// GetCollision loads a collision including a previously accepted decision,
// so a failed local move can be retried (ADR-003 §6.1).
func (p *Pool) GetCollision(ctx context.Context, id int64) (OpenCollision, string, string, error) {
	var c OpenCollision
	var winner, path string
	if p == nil || p.p == nil {
		return c, "", "", fmt.Errorf("GetCollision: pool not open")
	}
	err := p.p.QueryRow(ctx, `SELECT collision_id, project_prefix, display_path,
 held_event_id, held_uid, held_epoch, held_wall_clock_ts,
 incoming_event_id, incoming_uid, incoming_wall_clock_ts, detected_epoch,
 COALESCE(winner_event_id, ''), COALESCE(loser_new_path, '')
 FROM sync_node_collisions WHERE collision_id = $1`, id).Scan(
		&c.CollisionID, &c.ProjectPrefix, &c.DisplayPath,
		&c.HeldEventID, &c.HeldUID, &c.HeldEpoch, &c.HeldWallClockTS,
		&c.IncomingEventID, &c.IncomingUID, &c.IncomingWallClockTS, &c.DetectedEpoch,
		&winner, &path)
	if err == pgx.ErrNoRows {
		return c, "", "", nil
	}
	if err != nil {
		return c, "", "", fmt.Errorf("get collision %d: %s", id, redact.DSN(err.Error()))
	}
	return c, winner, path, nil
}

// CompleteCollisionMove records the local move after acceptance. Matching the
// winner prevents a retry from overwriting another operator's choice.
func (p *Pool) CompleteCollisionMove(ctx context.Context, id int64, winner, path string) error {
	if p == nil || p.p == nil {
		return fmt.Errorf("CompleteCollisionMove: pool not open")
	}
	tag, err := p.p.Exec(ctx, `UPDATE sync_node_collisions SET loser_new_path = $3
 WHERE collision_id = $1 AND status = 'resolved' AND winner_event_id = $2
 AND (COALESCE(loser_new_path, '') = '' OR loser_new_path = $3)`, id, winner, path)
	if err != nil {
		return fmt.Errorf("record collision %d local move: %s", id, redact.DSN(err.Error()))
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("collision %d decision changed; reload before retrying", id)
	}
	return nil
}
