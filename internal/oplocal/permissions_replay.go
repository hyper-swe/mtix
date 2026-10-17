// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"os"
	"time"
)

// ApplyPermissionIntent prepares new caller intent once and durably remembers IDs.
// Identical completed IDs never repeat grants, confirmations or publication.
func (s *State) applyPermissionIntent(ctx context.Context, scope string, intent PermissionBatch, prepare func(context.Context, PermissionBatch) (PermissionBatch, error), publish func(context.Context, PermissionBatch) error) error {
	if scope != "" && scope != intent.BoardID {
		return invalid("permission board scope conflicts")
	}
	if err := validatePermissionIntent(intent); err != nil {
		return err
	}
	return s.withPermissions(ctx, func(dir *os.File, host string) error {
		tx := permissionTransaction{ctx: ctx, dir: dir, host: host, publish: publish, boardID: scope}
		return s.applyPermissionIntentLocked(tx, intent, prepare)
	})
}
func validatePermissionIntent(intent PermissionBatch) error {
	if intent.CallerID != "" || intent.PublicationID != "" {
		return invalid("caller input contains derived caller identity")
	}
	if !permissionID(intent.ID) {
		return invalid("permission batch identity is invalid")
	}
	intent.Time = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	intent.NodeID = ""
	for i := range intent.Changes {
		if intent.Changes[i].PendingRegistration || intent.Changes[i].Outcome != "" {
			return invalid("permission intent contains derived registration state")
		}
	}
	return validatePermissionBatch(intent, true)
}
func (s *State) applyPermissionIntentLocked(tx permissionTransaction, intent PermissionBatch, prepare func(context.Context, PermissionBatch) (PermissionBatch, error)) error {
	if err := refuseCancelledPermission(tx.dir, tx.host, effectivePermissionCaller(intent)); err != nil {
		return err
	}
	if err := s.preemptPendingPermissions(tx, intent); err != nil {
		return err
	}
	receipts, err := readPermissionReplays(tx.dir, tx.host)
	if err != nil {
		return err
	}
	pending, err := readReplayPending(tx.dir, tx.host)
	if err != nil {
		return err
	}
	if operationErr := guardPermissionBoard(tx, pending); operationErr != nil {
		return operationErr
	}
	if _, err = validatePermissionReplayPending(tx.dir, tx.host, pending); err != nil {
		return err
	}
	hash, err := checkedPermissionIntentHash(intent, tx.host)
	if err != nil {
		return err
	}
	if intent.IntentHash != "" && intent.IntentHash != hash {
		return invalid("permission batch identity conflicts")
	}
	receipt, err := matchPermissionReplay(receipts, pending, effectivePermissionCaller(intent), hash)
	if err != nil {
		return err
	}
	if handled, resumeErr := s.resumePermissionIntent(tx, intent, pending, receipt); handled || resumeErr != nil {
		return resumeErr
	}
	if operationErr := s.recoverPermissionTransaction(tx); operationErr != nil {
		return operationErr
	}
	return s.preparePermissionIntent(tx, intent, hash, prepare)
}
func (s *State) resumePermissionIntent(tx permissionTransaction, intent PermissionBatch, pending permissionPending, receipt *permissionReplay) (bool, error) {
	if receipt != nil && receipt.Completed {
		if pending.Batch != nil && effectivePermissionCaller(*pending.Batch) == effectivePermissionCaller(intent) {
			return true, s.resumePendingPermissionBatch(tx, pending)
		}
		return true, nil
	}
	if pending.Batch != nil && effectivePermissionCaller(*pending.Batch) == effectivePermissionCaller(intent) {
		return true, s.resumePendingPermissionBatch(tx, pending)
	}
	if receipt != nil {
		return true, invalid("incomplete permission replay has no pending batch")
	}
	return false, nil
}

func (s *State) preparePermissionIntent(tx permissionTransaction, intent PermissionBatch, hash string, prepare func(context.Context, PermissionBatch) (PermissionBatch, error)) error {
	if prepare == nil {
		return invalid("permission intent preparation is required")
	}
	if err := freshPermissionOccurrence(tx, &intent); err != nil {
		return err
	}
	intent.IntentHash = hash
	canonical, err := prepare(tx.ctx, intent)
	if err != nil {
		return err
	}
	canonicalHash, err := checkedPermissionIntentHash(canonical, tx.host)
	if err != nil {
		return err
	}
	if canonicalHash != hash {
		return invalid("permission preparation changed caller intent")
	}
	for _, change := range canonical.Changes {
		if change.Outcome != "" {
			return invalid("permission preparation contains derived outcome")
		}
	}
	canonical.IntentHash = hash
	canonical, err = canonicalizeDeniedFile(tx, canonical)
	if err != nil {
		return err
	}
	if operationErr := validatePermissionBatch(canonical, true); operationErr != nil {
		return operationErr
	}
	if operationErr := checkPermissionReplayCapacity(tx.dir, tx.host, canonical); operationErr != nil {
		return operationErr
	}
	return s.acceptPermissions(tx, canonical)
}
func readReplayPending(dir *os.File, host string) (permissionPending, error) {
	data, err := readPermissionPendingBytes(dir)
	if err != nil {
		return permissionPending{}, err
	}
	pending, err := decodePermissionPending(data, host)
	if err != nil {
		return pending, err
	}
	return pending, validateReplayPendingHost(pending, host)
}
func matchPermissionReplay(receipts []permissionReplay, p permissionPending, id, hash string) (*permissionReplay, error) {
	for i := range receipts {
		if effectivePermissionCaller(receipts[i].Batch) == id {
			if receipts[i].Batch.IntentHash != hash {
				return nil, invalid("permission batch identity conflicts")
			}
			if p.Batch != nil && effectivePermissionCaller(*p.Batch) == id && !samePermissionBatch(receipts[i].Batch, *p.Batch) {
				return nil, invalid("permission replay pending conflicts")
			}
			return &receipts[i], nil
		}
	}
	if p.Batch != nil && effectivePermissionCaller(*p.Batch) == id && p.Batch.IntentHash != hash {
		return nil, invalid("permission batch identity conflicts")
	}
	return nil, nil
}
