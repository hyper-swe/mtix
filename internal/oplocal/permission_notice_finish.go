// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import "errors"

func (s *State) publishPermissionNotice(tx permissionTransaction, p permissionPending) (permissionPending, error) {
	if p.Batch.NodeID == "" || p.NoticeOutcome == "undeliverable" {
		return p, nil
	}
	err := tx.publish(tx.ctx, *p.Batch)
	if !errors.Is(err, ErrPermissionNoticeUndeliverable) {
		return p, err
	}
	p.NoticeOutcome = "undeliverable"
	return p, s.commitPermissionRecord(tx.dir, "permissions-pending.json", tx.host, p)
}

func (s *State) finalizePermissionNotice(tx permissionTransaction, p permissionPending) error {
	if p.NoticeOutcome == "" {
		return s.finalizeConfirmations(tx.dir, tx.host, *p.Batch)
	}
	segments, err := readConfirmationSegments(tx.dir, tx.host)
	if err != nil {
		return err
	}
	for i, segment := range segments {
		changed := false
		for j, row := range segment.rows {
			if row.ID == p.Batch.ID && row.BoardLine == "pending" {
				segment.rows[j].BoardLine = p.NoticeOutcome
				changed = true
			}
		}
		if !changed {
			continue
		}
		data, encodeErr := encodeConfirmations(segment.rows)
		if encodeErr != nil {
			return encodeErr
		}
		if err := s.commitAlertData(tx.dir, confirmationFilename(i), data, "final:"+confirmationFilename(i)); err != nil {
			return err
		}
	}
	return nil
}
