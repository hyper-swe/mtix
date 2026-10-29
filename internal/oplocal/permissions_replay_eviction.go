// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import "time"

func replayCompletionTime(receipt permissionReplay) time.Time {
	if !receipt.CompletedAt.IsZero() {
		return receipt.CompletedAt
	}
	return receipt.Batch.Time
}
func trimPermissionReplays(rows []permissionReplay, host, pinned string) ([]permissionReplay, error) {
	for {
		if _, err := encodePermissionReplayReservation(rows, host); err == nil {
			return rows, nil
		}
		oldest := -1
		for i, row := range rows {
			if !row.Completed || row.Batch.ID == pinned {
				continue
			}
			if oldest < 0 || replayCompletionTime(row).Before(replayCompletionTime(rows[oldest])) {
				oldest = i
			}
		}
		if oldest < 0 {
			return nil, invalid("permission replay inflight receipts exceed capacity")
		}
		rows = append(append([]permissionReplay{}, rows[:oldest]...), rows[oldest+1:]...)
	}
}
func (s *State) completePermissionReplay(row permissionReplay, completed bool) (permissionReplay, error) {
	if !completed || row.Completed {
		return row, nil
	}
	now := s.permissionClock().UTC()
	if now.IsZero() || now.Year() < 1 || now.Year() > 9999 {
		return row, invalid("permission completion time is invalid")
	}
	row.Completed = true
	row.CompletedAt = now
	return row, nil
}

func encodePermissionReplayReservation(rows []permissionReplay, host string) ([]byte, error) {
	reserved := append([]permissionReplay{}, rows...)
	for i := range reserved {
		if !reserved[i].Completed {
			reserved[i].CompletedAt = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
			reserved[i].Cancelled = true
		}
	}
	return encodePermissionReplays(reserved, host)
}
