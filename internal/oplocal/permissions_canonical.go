// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"crypto/rand"
	"encoding/hex"
)

func canonicalizeDeniedFile(tx permissionTransaction, b PermissionBatch) (PermissionBatch, error) {
	if b.Mode != "file" {
		return b, nil
	}
	_, grants, err := readPermissionGrants(tx.dir, tx.host)
	if err != nil {
		return b, err
	}
	b.Changes = append([]PermissionChange{}, b.Changes...)
	for i, change := range b.Changes {
		if change.Action != "allow" {
			continue
		}
		for _, grant := range grants {
			if grant.BoardID == b.BoardID && grant.AgentID == change.AgentID && (grant.Denied || grant.Quarantined) {
				b.Changes[i].Outcome = "denied_requires_operator"
				if grant.Quarantined {
					b.Changes[i].Outcome = "quarantined_requires_operator"
				}
				b.Changes[i].PendingRegistration = false
				break
			}
		}
	}
	return b, nil
}
func freshPermissionOccurrence(tx permissionTransaction, b *PermissionBatch) error {
	segments, err := readConfirmationSegments(tx.dir, tx.host)
	if err != nil {
		return err
	}
	rows, err := logicalConfirmations(segments, permissionPending{}, tx.host)
	if err != nil {
		return err
	}
	receipts, err := readPermissionReplays(tx.dir, tx.host)
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, row := range rows {
		used[row.ID] = true
	}
	for _, receipt := range receipts {
		used[receipt.Batch.ID] = true
	}
	caller := effectivePermissionCaller(*b)
	for used[b.ID] {
		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			return failure("permission occurrence identifier", err)
		}
		b.ID = hex.EncodeToString(id)
		b.CallerID = caller
	}
	return nil
}
