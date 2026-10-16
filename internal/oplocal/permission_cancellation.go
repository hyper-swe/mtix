// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"errors"
)

// ErrPermissionNoticeUndeliverable identifies a removed publication target.
var ErrPermissionNoticeUndeliverable = errors.New("permission notice target is unavailable")

func permissionDenyOnly(batch PermissionBatch) bool {
	if len(batch.Changes) == 0 {
		return false
	}
	for _, change := range batch.Changes {
		if change.Action != "deny" {
			return false
		}
	}
	return true
}

func (s *State) preemptPendingPermissions(tx permissionTransaction, incoming PermissionBatch) error {
	if !permissionDenyOnly(incoming) {
		return nil
	}
	p, err := readReplayPending(tx.dir, tx.host)
	if err != nil || p.Batch == nil {
		return err
	}
	if incoming.ID != "" && effectivePermissionCaller(incoming) == effectivePermissionCaller(*p.Batch) {
		return nil
	}
	receipt, err := validatePermissionReplayPending(tx.dir, tx.host, p)
	if err != nil {
		return err
	}
	if receipt != nil && receipt.Completed {
		return s.commitPermissionRecord(tx.dir, "permissions-pending.json", tx.host, clearedPermissionPending(p))
	}
	agents := []string{}
	for _, change := range incoming.Changes {
		if incoming.BoardID == p.Batch.BoardID {
			agents = append(agents, change.AgentID)
		}
	}
	return s.cancelPendingPermissions(tx, p, "priority operator deny: older uncommitted allows cancelled or quarantined", agents)
}

func (s *State) cancelPendingPermissions(tx permissionTransaction, p permissionPending, reason string, agents []string) error {
	row, err := readPermissionCancellation(tx.dir, tx.host, effectivePermissionCaller(*p.Batch))
	if err != nil {
		return err
	}
	if row == nil {
		row = &permissionCancellation{Pending: p, Reason: reason, At: s.permissionClock().UTC(), BoardLine: "quarantined", DeniedAgents: agents}
		if row.At.IsZero() || row.At.Year() < 1 || row.At.Year() > 9999 {
			return invalid("cancellation time is invalid")
		}
		if p.Batch.BoardID == tx.boardID && p.Batch.NodeID != "" && s.permissionNoticeProbe != nil {
			unavailable, probeErr := s.permissionNoticeProbe(tx.ctx, *p.Batch)
			if probeErr == nil && unavailable {
				row.BoardLine = "undeliverable"
			}
		}
		if err := s.commitPermissionRecord(tx.dir, permissionCancellationName(effectivePermissionCaller(*p.Batch)), tx.host, row); err != nil {
			return err
		}
	}
	if !samePermissionBatch(*row.Pending.Batch, *p.Batch) {
		return invalid("cancellation pending batch conflicts")
	}
	return s.finishPermissionCancellation(tx, *row)
}

func (s *State) finishPermissionCancellation(tx permissionTransaction, row permissionCancellation) error {
	p := row.Pending
	data, grants, err := readPermissionGrants(tx.dir, tx.host)
	if err != nil {
		return err
	}
	projected, err := permissionPendingGrantProjection(data, grants, p, tx.host)
	if err != nil {
		return err
	}
	if err := s.commitPermissionRecord(tx.dir, "grants.json", tx.host, projected); err != nil {
		return err
	}
	if err := s.projectCancellationConfirmations(tx, row); err != nil {
		return err
	}
	if err := s.cancelPermissionReplay(tx, *p.Batch); err != nil {
		return err
	}
	return s.commitPermissionRecord(tx.dir, "permissions-pending.json", tx.host, clearedPermissionPending(p))
}
