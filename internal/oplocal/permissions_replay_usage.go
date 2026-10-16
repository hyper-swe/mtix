// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"bytes"
	"context"
	"errors"
	"os"
)

// PermissionReplayUsage reports bounded host-local replay receipt capacity.
type PermissionReplayUsage struct{ Count, Bytes, MaxCount, MaxBytes int }

// PermissionReplayUsage reads protected capacity without creating or clearing state.
func (s *State) PermissionReplayUsage(ctx context.Context) (usage PermissionReplayUsage, err error) {
	usage = PermissionReplayUsage{MaxCount: maxPermissionReplays, MaxBytes: maxRecordSize}
	if contextErr := ctx.Err(); contextErr != nil {
		return usage, contextErr
	}
	dir, err := s.directory(false)
	if missingState(err) {
		return usage, nil
	}
	if err != nil {
		return usage, err
	}
	defer closeWith(dir, &err)
	host, err := s.hostID(dir, false)
	if err != nil {
		return usage, err
	}
	for range 4 {
		if contextErr := ctx.Err(); contextErr != nil {
			return usage, contextErr
		}
		before, readErr := readPermissionPendingBytes(dir)
		if readErr != nil {
			return usage, readErr
		}
		rows, size, readErr := readPermissionReplayLedger(dir, host)
		pending, pendingErr := decodePermissionPending(before, host)
		if readErr == nil {
			readErr = pendingErr
		}
		if readErr == nil {
			_, readErr = validatePermissionReplayPending(dir, host, pending)
		}
		after, afterErr := readPermissionPendingBytes(dir)
		if afterErr != nil {
			return usage, afterErr
		}
		if bytes.Equal(before, after) {
			usage.Count = len(rows)
			usage.Bytes = size
			return usage, readErr
		}
	}
	return usage, invalid("permission replay usage changed repeatedly")
}
func readPermissionReplayLedger(dir *os.File, host string) ([]permissionReplay, int, error) {
	data, err := readData(dir, "permissions-replays.json")
	if errors.Is(err, os.ErrNotExist) {
		return []permissionReplay{}, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var envelope record
	if operationErr := decodeAlertJSON(data, &envelope); operationErr != nil {
		return nil, 0, operationErr
	}
	if envelope.HostID != host {
		return nil, 0, invalid("permission replay ledger belongs to another host")
	}
	var rows []permissionReplay
	if operationErr := decodeAlertJSON(envelope.Data, &rows); operationErr != nil {
		return nil, 0, operationErr
	}
	return rows, len(data), validatePermissionReplays(rows, host)
}
