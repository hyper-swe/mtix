// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"os"
)

// PendingPermissionBatches lists the host-global accepted batch without writing.
func (s *State) PendingPermissionBatches(ctx context.Context) (batches []PermissionBatch, err error) {
	batches = []PermissionBatch{}
	if contextErr := ctx.Err(); contextErr != nil {
		return batches, contextErr
	}
	dir, err := s.directory(false)
	if missingState(err) {
		return batches, nil
	}
	if err != nil {
		return batches, err
	}
	defer closeWith(dir, &err)
	host, err := s.hostID(dir, false)
	if err != nil {
		return batches, err
	}
	p, err := readReplayPending(dir, host)
	if err != nil {
		return batches, err
	}
	if _, err = validatePermissionReplayPending(dir, host, p); err != nil {
		return batches, err
	}
	if p.Batch != nil {
		batches = append(batches, *p.Batch)
	}
	return batches, nil
}

// CancelPermissionBatch cancels only the named uncommitted batch. It cannot grant
// permission; accepted denies and committed preimages survive cancellation.
func (s *State) CancelPermissionBatch(ctx context.Context, id string) error {
	if !permissionID(id) {
		return invalid("recovery batch identity is invalid")
	}
	return s.withPermissions(ctx, func(dir *os.File, host string) error {
		tx := permissionTransaction{ctx: ctx, dir: dir, host: host}
		p, err := readReplayPending(dir, host)
		if err != nil {
			return err
		}
		if p.Batch == nil || effectivePermissionCaller(*p.Batch) != id {
			return invalid("named permission batch is not pending")
		}
		receipt, err := validatePermissionReplayPending(dir, host, p)
		if err != nil {
			return err
		}
		if receipt != nil && receipt.Completed && !receipt.Cancelled {
			return invalid("permission batch already committed; its confirmation cannot be cancelled")
		}
		return s.cancelPendingPermissions(tx, p, "explicit operator recovery cancelled uncommitted batch; accepted denies retained", nil)
	})
}
