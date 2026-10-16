// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"bytes"
	"context"
	"os"
)

// PermissionReplayResult returns a full retained completed canonical batch.
// Missing, evicted or inflight callers are explicitly unavailable; history is not rebuilt.
func (s *State) PermissionReplayResult(ctx context.Context, boardID, callerID string) (batch PermissionBatch, available bool, err error) {
	if !validBoardID(boardID) || !permissionID(callerID) {
		return batch, false, invalid("permission replay lookup identity is invalid")
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return batch, false, contextErr
	}
	dir, err := s.directory(false)
	if missingState(err) {
		return batch, false, nil
	}
	if err != nil {
		return batch, false, err
	}
	defer closeWith(dir, &err)
	host, err := s.hostID(dir, false)
	if err != nil {
		return batch, false, err
	}
	for range 4 {
		if contextErr := ctx.Err(); contextErr != nil {
			return batch, false, contextErr
		}
		rows, coherent, readErr := readPermissionResultSnapshot(dir, host)
		if !coherent {
			continue
		}
		if readErr != nil {
			return batch, false, readErr
		}
		return completedPermissionResult(rows, boardID, callerID)
	}
	return batch, false, invalid("permission replay result changed repeatedly")
}

func completedPermissionResult(rows []permissionReplay, boardID, callerID string) (PermissionBatch, bool, error) {
	for _, row := range rows {
		if effectivePermissionCaller(row.Batch) == callerID && row.Batch.BoardID == boardID && row.Cancelled {
			return PermissionBatch{}, false, invalid("permission batch was cancelled or quarantined")
		}
		if effectivePermissionCaller(row.Batch) == callerID && row.Batch.BoardID == boardID && row.Completed {
			return row.Batch, true, nil
		}
	}
	return PermissionBatch{}, false, nil
}

func readPermissionResultSnapshot(dir *os.File, host string) ([]permissionReplay, bool, error) {
	before, err := readPermissionPendingBytes(dir)
	if err != nil {
		return nil, true, err
	}
	rows, readErr := readPermissionReplays(dir, host)
	pending, pendingErr := decodePermissionPending(before, host)
	if readErr == nil {
		readErr = pendingErr
	}
	if readErr == nil {
		_, readErr = validatePermissionReplayPending(dir, host, pending)
	}
	after, err := readPermissionPendingBytes(dir)
	if err != nil {
		return nil, true, err
	}
	return rows, bytes.Equal(before, after), readErr
}
