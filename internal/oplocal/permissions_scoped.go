// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"time"
)

// NewWithPermissionClock constructs immutable injected completion timing.
func NewWithPermissionClock(env Env, clock func() time.Time) *State {
	if clock == nil {
		panic("permission clock must not be nil")
	}
	return &State{env: env, permissionClock: clock}
}

// ApplyPermissions preserves the legacy operator batch path.
func (s *State) ApplyPermissions(ctx context.Context, b PermissionBatch, publish func(context.Context, PermissionBatch) error) error {
	return s.applyPermissionsScoped(ctx, "", b, publish)
}

// ApplyPermissionsScoped prevents recovery against a different board store.
func (s *State) ApplyPermissionsScoped(ctx context.Context, boardID string, b PermissionBatch, publish func(context.Context, PermissionBatch) error) error {
	return s.applyPermissionsScoped(ctx, boardID, b, publish)
}

// ApplyPermissionIntent prepares or replays caller intent under the host mutex.
func (s *State) ApplyPermissionIntent(ctx context.Context, b PermissionBatch, prepare func(context.Context, PermissionBatch) (PermissionBatch, error), publish func(context.Context, PermissionBatch) error) error {
	return s.applyPermissionIntent(ctx, "", b, prepare, publish)
}

// ApplyPermissionIntentScoped validates the store board before recovering pending work.
func (s *State) ApplyPermissionIntentScoped(ctx context.Context, boardID string, b PermissionBatch, prepare func(context.Context, PermissionBatch) (PermissionBatch, error), publish func(context.Context, PermissionBatch) error) error {
	return s.applyPermissionIntent(ctx, boardID, b, prepare, publish)
}
func guardPermissionBoard(tx permissionTransaction, p permissionPending) error {
	if tx.boardID != "" && p.Batch != nil && p.Batch.BoardID != tx.boardID {
		return invalid("pending permission batch belongs to another board")
	}
	return nil
}
func (s *State) recoverPermissionTransaction(tx permissionTransaction) error {
	p, err := readReplayPending(tx.dir, tx.host)
	if err != nil {
		return err
	}
	if operationErr := guardPermissionBoard(tx, p); operationErr != nil {
		return operationErr
	}
	if p.Batch == nil {
		return nil
	}
	return s.finishPermissions(tx, p)
}
func effectivePermissionCaller(b PermissionBatch) string {
	if b.CallerID != "" {
		return b.CallerID
	}
	return b.ID
}
