// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

type collisionMove struct {
	accept   func(context.Context) error
	renumber func(context.Context, string) (string, error)
	finish   func(context.Context, string) error
}

// resolveCollisionMove records the hub decision before moving any local task.
// A durable decision plus an idempotent local move makes interrupted runs resumable.
// After acceptance, errors name the failed stage (local renumber or hub completion),
// wrap the cause, and print the retry command (MTIX-107.46.2).
func resolveCollisionMove(ctx context.Context, c transport.OpenCollision, choice, loser string, move collisionMove) (string, error) {
	if err := move.accept(ctx); err != nil {
		return "", fmt.Errorf("resolve collision: %w", err)
	}
	path, err := move.renumber(ctx, loser)
	step := "local renumber"
	if err == nil {
		step = "recording completion on the hub"
		err = move.finish(ctx, path)
	}
	if err != nil {
		return "", fmt.Errorf("hub accepted collision %d but %s failed: %w; fix the cause, then run on the client that owns the losing node: mtix sync collisions resolve %d --winner %s", c.CollisionID, step, err, c.CollisionID, choice)
	}
	return path, nil
}

// collisionHub is the part of the hub connection the resolve flow uses; the
// transport pool satisfies it, and tests supply a fake.
type collisionHub interface {
	GetCollision(ctx context.Context, id int64) (transport.OpenCollision, string, string, error)
	ResolveCollision(ctx context.Context, id int64, winner, loserNewPath, by string) (bool, error)
	CompleteCollisionMove(ctx context.Context, id int64, winner, path string) error
}

// acceptCollision also verifies the winning decision when another operator
// won the guarded update, or a previous invocation already accepted it.
func acceptCollision(ctx context.Context, hub collisionHub, collisionID int64, winner string) error {
	accepted, err := hub.ResolveCollision(ctx, collisionID, winner, "", syncActor())
	if err != nil {
		return err
	}
	if accepted {
		return nil
	}
	_, actual, _, err := hub.GetCollision(ctx, collisionID)
	if err != nil {
		return err
	}
	if actual != winner {
		return fmt.Errorf("collision %d was resolved with a different winner; local store unchanged", collisionID)
	}
	return nil
}

// locateCollisionLoser reports where the losing task is in THIS local store, or
// an error naming the cause. It is read-only and runs BEFORE the hub accepts,
// so a run on the wrong client leaves the hub row open (MTIX-107.46).
func locateCollisionLoser(ctx context.Context, uid string) (string, error) {
	current, err := app.store.LiveNodeIDByUID(ctx, uid)
	if errors.Is(err, model.ErrNotFound) {
		return "", fmt.Errorf("mtix sync collisions resolve: the losing node (uid %s) is not in this local store; "+
			"run resolve on the client that owns it", format.EventID(uid))
	}
	if err != nil {
		return "", fmt.Errorf("load losing node in this local store: %w", err)
	}
	return current, nil
}

// renumberCollisionLoser recognizes an already committed local move, including
// a crash before its new path was recorded on the hub. Uids survive renumbering.
func renumberCollisionLoser(ctx context.Context, uid, contested string) (string, error) {
	current, err := locateCollisionLoser(ctx, uid)
	if err != nil {
		return "", err
	}
	if current != contested {
		return current, nil
	}
	return app.store.RenumberForHubRejection(ctx, uid)
}
