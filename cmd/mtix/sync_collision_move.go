// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

type collisionMove struct {
	accept   func(context.Context) error
	renumber func(context.Context, string) (string, error)
	finish   func(context.Context, string) error
}

// resolveCollisionMove records the hub decision before moving any local task.
// A durable decision plus an idempotent local move makes interrupted runs resumable.
func resolveCollisionMove(ctx context.Context, c transport.OpenCollision, choice, loser string, move collisionMove) (string, error) {
	if err := move.accept(ctx); err != nil {
		return "", fmt.Errorf("resolve collision: %w", err)
	}
	path, err := move.renumber(ctx, loser)
	if err == nil {
		err = move.finish(ctx, path)
	}
	if err != nil {
		return "", fmt.Errorf("hub accepted collision %d but local completion failed: %w; fix the cause, then run on the client that owns the losing node: mtix sync collisions resolve %d --winner %s", c.CollisionID, err, c.CollisionID, choice)
	}
	return path, nil
}

// acceptCollision also verifies the winning decision when another operator
// won the guarded update, or a previous invocation already accepted it.
func acceptCollision(ctx context.Context, pool *transport.Pool, id int64, winner string) error {
	accepted, err := pool.ResolveCollision(ctx, id, winner, "", syncActor())
	if err != nil {
		return err
	}
	if accepted {
		return nil
	}
	_, actual, _, err := pool.GetCollision(ctx, id)
	if err != nil {
		return err
	}
	if actual != winner {
		return fmt.Errorf("collision %d was resolved with a different winner; local store unchanged", id)
	}
	return nil
}

// renumberCollisionLoser recognizes an already committed local move, including
// a crash before its new path was recorded on the hub. Uids survive renumbering.
func renumberCollisionLoser(ctx context.Context, uid, contested string) (string, error) {
	var current string
	if err := app.store.QueryRow(ctx, `SELECT id FROM nodes WHERE uid = ?`, uid).Scan(&current); err != nil {
		return "", fmt.Errorf("load losing node in this local store: %w", err)
	}
	if current != contested {
		return current, nil
	}
	return app.store.RenumberForHubRejection(ctx, uid)
}
