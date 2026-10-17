// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import "slices"

func cancellationConfirmationLine(row permissionCancellation, agent string) string {
	if row.BoardLine == "undeliverable" {
		return row.BoardLine
	}
	if slices.Contains(row.DeniedAgents, agent) {
		return "cancelled"
	}
	return "quarantined"
}

func (s *State) projectCancellationConfirmations(tx permissionTransaction, row permissionCancellation) error {
	p := row.Pending
	segments, err := readConfirmationSegments(tx.dir, tx.host)
	if err != nil {
		return err
	}
	exists := cancellationBatchRecorded(segments, p.Batch.ID)
	if !exists {
		if operationErr := s.projectConfirmations(tx.dir, tx.host, p, segments); operationErr != nil {
			return operationErr
		}
		segments, err = readConfirmationSegments(tx.dir, tx.host)
		if err != nil {
			return err
		}
	}
	for i, segment := range segments {
		if !cancelConfirmationRows(segment.rows, row) {
			continue
		}
		data, encodeErr := encodeConfirmations(segment.rows)
		if encodeErr != nil {
			return encodeErr
		}
		if err := s.commitAlertData(tx.dir, confirmationFilename(i), data, "cancel:"+confirmationFilename(i)); err != nil {
			return err
		}
	}
	return nil
}

func (s *State) cancelPermissionReplay(tx permissionTransaction, b PermissionBatch) error {
	if b.IntentHash == "" {
		return nil
	}
	rows, err := readPermissionReplays(tx.dir, tx.host)
	if err != nil {
		return err
	}
	found := false
	for i := range rows {
		if rows[i].Batch.ID == b.ID {
			rows[i].Cancelled = true
			rows[i], err = s.completePermissionReplay(rows[i], true)
			if err != nil {
				return err
			}
			found = true
		}
	}
	if !found {
		return nil
	}
	rows, err = trimPermissionReplays(rows, tx.host, b.ID)
	if err != nil {
		return err
	}
	data, err := encodePermissionReplays(rows, tx.host)
	if err != nil {
		return err
	}
	return s.commitAlertData(tx.dir, "permissions-replays.json", data, "permission-replay-cancelled")
}

func cancellationBatchRecorded(segments [3]confirmationSegment, id string) bool {
	for _, segment := range segments {
		for _, row := range segment.rows {
			if row.ID == id {
				return true
			}
		}
	}
	return false
}

func cancelConfirmationRows(rows []Confirmation, cancellation permissionCancellation) bool {
	changed := false
	for i, row := range rows {
		if row.ID == cancellation.Pending.Batch.ID {
			rows[i].BoardLine = cancellationConfirmationLine(cancellation, row.AgentID)
			changed = true
		}
	}
	return changed
}
