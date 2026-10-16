// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import "os"

func (s *State) projectConfirmations(dir *os.File, host string, p permissionPending, segments [3]confirmationSegment) error {
	rows, err := checkedBatchConfirmations(*p.Batch, host)
	if err != nil {
		return err
	}
	complete, err := confirmationBatchExists(segments, rows)
	if err != nil || complete {
		return err
	}
	if p.Rotate {
		return s.rotateConfirmations(dir, p, segments, rows)
	}
	if alertHash(segments[0].data) != p.ActiveHash {
		return invalid("confirmation preimage changed")
	}
	data, err := encodeConfirmations(append(append([]Confirmation{}, segments[0].rows...), rows...))
	if err != nil {
		return err
	}
	return s.commitAlertData(dir, confirmationFilename(0), data, confirmationFilename(0))
}
func (s *State) rotateConfirmations(dir *os.File, p permissionPending, segments [3]confirmationSegment, rows []Confirmation) error {
	copied := alertHash(segments[1].data) == p.ActiveHash
	if !copied {
		if alertHash(segments[0].data) != p.ActiveHash || alertHash(segments[1].data) != p.ArchiveHash {
			return invalid("confirmation rotation preimage changed")
		}
		if segments[1].data != nil {
			if err := s.commitAlertData(dir, confirmationFilename(2), segments[1].data, confirmationFilename(2)); err != nil {
				return err
			}
		}
		if segments[0].data != nil {
			if err := s.commitAlertData(dir, confirmationFilename(1), segments[0].data, confirmationFilename(1)); err != nil {
				return err
			}
		}
	}
	data, err := encodeConfirmations(rows)
	if err != nil {
		return err
	}
	return s.commitAlertData(dir, confirmationFilename(0), data, confirmationFilename(0))
}

// compatibleConfirmation permits only the monotonic publication-state transition.
func compatibleConfirmation(a, b Confirmation) bool {
	if permissionFinalLine(a.BoardLine) && b.BoardLine == "pending" {
		a.BoardLine = "pending"
	}
	if permissionFinalLine(b.BoardLine) && a.BoardLine == "pending" {
		b.BoardLine = "pending"
	}
	if permissionCancellationLine(a.BoardLine) && b.BoardLine == "skipped_no_node" {
		a.BoardLine = "skipped_no_node"
	}
	if permissionCancellationLine(b.BoardLine) && a.BoardLine == "skipped_no_node" {
		b.BoardLine = "skipped_no_node"
	}
	return sameConfirmation(a, b)
}
func (s *State) finalizeConfirmations(dir *os.File, host string, b PermissionBatch) error {
	if b.NodeID == "" {
		return nil
	}
	segments, err := readConfirmationSegments(dir, host)
	if err != nil {
		return err
	}
	for i, segment := range segments {
		changed := false
		for j, row := range segment.rows {
			if row.ID == b.ID && row.BoardLine == "pending" {
				segment.rows[j].BoardLine = "published"
				changed = true
			}
		}
		if !changed {
			continue
		}
		data, err := encodeConfirmations(segment.rows)
		if err != nil {
			return err
		}
		if operationErr := s.commitAlertData(dir, confirmationFilename(i), data, "final:"+confirmationFilename(i)); operationErr != nil {
			return operationErr
		}
	}
	return nil
}

func permissionFinalLine(line string) bool {
	return line == "published" || line == "cancelled" || line == "quarantined" || line == "undeliverable"
}

func permissionCancellationLine(line string) bool {
	return line == "cancelled" || line == "quarantined"
}
