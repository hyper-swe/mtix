// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"errors"
	"os"
	"time"
)

type permissionCancellation struct {
	Pending      permissionPending `json:"pending"`
	Reason       string            `json:"reason"`
	At           time.Time         `json:"cancelled_at"`
	BoardLine    string            `json:"board_line"`
	DeniedAgents []string          `json:"denied_agents"`
}

func permissionCancellationName(id string) string { return "permission-cancelled-" + id + ".json" }

// Terminal caller tombstones are separate protected records: replay-ring eviction
// never revives a cancelled caller. They retain the canonical audit/preimage.
func readPermissionCancellation(dir *os.File, host, id string) (*permissionCancellation, error) {
	if !permissionID(id) {
		return nil, invalid("cancellation identity is invalid")
	}
	data, err := readData(dir, permissionCancellationName(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var envelope record
	if err := decodeAlertJSON(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.HostID != host {
		return nil, invalid("cancellation belongs to another host")
	}
	var row permissionCancellation
	if err := decodeAlertJSON(envelope.Data, &row); err != nil {
		return nil, err
	}
	if err := validatePermissionCancellation(row, host, id); err != nil {
		return nil, err
	}
	return &row, nil
}

func refuseCancelledPermission(dir *os.File, host, id string) error {
	row, err := readPermissionCancellation(dir, host, id)
	if err != nil {
		return err
	}
	if row != nil {
		return invalid("permission batch was cancelled or quarantined; use a fresh caller id after explicit operator review")
	}
	return nil
}

func validatePermissionCancellation(row permissionCancellation, host, id string) error {
	if row.Pending.Batch == nil || effectivePermissionCaller(*row.Pending.Batch) != id || !permissionText(row.Reason) || row.At.IsZero() {
		return invalid("cancellation audit is invalid")
	}
	if row.BoardLine != "quarantined" && row.BoardLine != "undeliverable" {
		return invalid("cancellation notice state is invalid")
	}
	if err := validatePermissionPending(row.Pending); err != nil {
		return err
	}
	if err := validateReplayPendingHost(row.Pending, host); err != nil {
		return err
	}
	return validatePermissionBefore(row.Pending, host)
}
