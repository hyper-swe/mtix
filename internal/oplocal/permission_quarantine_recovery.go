// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

func (s *State) recoverPermissionCancellation(tx permissionTransaction, p permissionPending) (bool, error) {
	if err := tx.ctx.Err(); err != nil {
		return true, err
	}
	row, err := readPermissionCancellation(tx.dir, tx.host, effectivePermissionCaller(*p.Batch))
	if err != nil {
		return true, err
	}
	if row != nil {
		return true, s.finishPermissionCancellation(tx, *row)
	}
	if operationErr := guardPermissionBoard(tx, p); operationErr != nil {
		return true, operationErr
	}
	quarantine, err := legacyPermissionQuarantineNeeded(tx, p)
	if err != nil {
		return true, err
	}
	if quarantine {
		return true, s.cancelPendingPermissions(tx, p, "legacy uncommitted allow has no committed preimage; quarantine hold requires fresh operator allow", nil)
	}
	return false, nil
}

func legacyPermissionQuarantineNeeded(tx permissionTransaction, p permissionPending) (bool, error) {
	if p.BeforeGrants != nil || len(permissionAllowAgents(*p.Batch)) == 0 {
		return false, nil
	}
	receipt, err := validatePermissionReplayPending(tx.dir, tx.host, p)
	if err != nil {
		return false, err
	}
	return receipt == nil || !receipt.Completed, nil
}

func (s *State) resumePendingPermissionBatch(tx permissionTransaction, p permissionPending) error {
	if err := s.finishPermissions(tx, p); err != nil {
		return err
	}
	return refuseCancelledPermission(tx.dir, tx.host, effectivePermissionCaller(*p.Batch))
}
