// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
)

type permissionSnapshot struct {
	grants []AgentGrant
	rows   []Confirmation
}

// AgentGrants reads committed allows and accepted denies from a protected snapshot.
// Reads never take the mutation lock, so a publication callback may safely inspect state.
func (s *State) AgentGrants() ([]AgentGrant, error) {
	snapshot, err := s.permissionSnapshot(context.Background())
	return snapshot.grants, err
}

// Confirmations reads retained and accepted pending immutable records without locking.
func (s *State) Confirmations(ctx context.Context) ([]Confirmation, error) {
	snapshot, err := s.permissionSnapshot(ctx)
	return snapshot.rows, err
}
func (s *State) permissionSnapshot(ctx context.Context) (snapshot permissionSnapshot, err error) {
	snapshot = permissionSnapshot{grants: []AgentGrant{}, rows: []Confirmation{}}
	if operationErr := ctx.Err(); operationErr != nil {
		return snapshot, operationErr
	}
	dir, err := s.directory(false)
	if missingState(err) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	defer closeWith(dir, &err)
	host, err := s.hostID(dir, false)
	if err != nil {
		return snapshot, err
	}
	for range 4 {
		if operationErr := ctx.Err(); operationErr != nil {
			return snapshot, operationErr
		}
		before, readErr := readPermissionPendingBytes(dir)
		if readErr != nil {
			return snapshot, readErr
		}
		snapshot, err = readPermissionSnapshot(dir, host, before, s.permissionReadStage)
		after, readErr := readPermissionPendingBytes(dir)
		if readErr != nil {
			return snapshot, readErr
		}
		if bytes.Equal(before, after) {
			return snapshot, err
		}
	}
	return snapshot, invalid("permission snapshot changed repeatedly")
}
func readPermissionPendingBytes(dir *os.File) ([]byte, error) {
	data, err := readData(dir, "permissions-pending.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}
func decodePermissionPending(data []byte, host string) (permissionPending, error) {
	var pending permissionPending
	if data == nil {
		return pending, nil
	}
	var envelope record
	if err := decodeAlertJSON(data, &envelope); err != nil {
		return pending, err
	}
	if envelope.HostID != host {
		return pending, invalid("permission transaction belongs to another host")
	}
	if err := decodeAlertJSON(envelope.Data, &pending); err != nil {
		return pending, err
	}
	if err := validatePermissionPending(pending); err != nil {
		return pending, err
	}
	return pending, validatePermissionBefore(pending, host)
}
func validatePermissionPending(p permissionPending) error {
	if err := validatePermissionNoticeOutcome(p); err != nil {
		return err
	}
	if p.Generation != "" && !permissionID(p.Generation) {
		return invalid("permission generation is invalid")
	}
	if p.Batch == nil {
		return validateClearedPermissionPending(p)
	}
	if p.CompletedID != "" {
		return invalid("pending permission generation conflicts")
	}
	if err := validatePermissionBatch(*p.Batch, true); err != nil {
		return err
	}
	if !validAlertHash(p.ActiveHash) || !validAlertHash(p.ArchiveHash) {
		return invalid("permission preimages are invalid")
	}
	return nil
}
func readPermissionSnapshot(dir *os.File, host string, data []byte, stage func(string)) (permissionSnapshot, error) {
	snapshot := permissionSnapshot{rows: []Confirmation{}}
	pending, err := decodePermissionPending(data, host)
	if err != nil {
		return snapshot, err
	}
	receipt, err := validatePermissionReplayPending(dir, host, pending)
	if err != nil {
		return snapshot, err
	}
	grantsData, grants, err := readPermissionGrants(dir, host)
	if err != nil {
		return snapshot, err
	}
	if stage != nil {
		stage("after:grants")
	}
	segments, err := readConfirmationSegments(dir, host)
	if err != nil {
		return snapshot, err
	}
	if pending.Batch != nil {
		projected, projectionErr := permissionSnapshotGrantProjection(grantsData, grants, pending, host, receipt)
		if projectionErr != nil {
			return snapshot, projectionErr
		}
		if operationErr := json.Unmarshal(projected["agent_grants"], &grants); operationErr != nil {
			return snapshot, operationErr
		}
	}
	snapshot.grants = grants
	historyPending := pending
	if receipt != nil && receipt.Completed {
		historyPending = permissionPending{}
	}
	rows, err := logicalConfirmations(segments, historyPending, host)
	snapshot.rows = rows
	return snapshot, err
}
func logicalConfirmations(segments [3]confirmationSegment, p permissionPending, host string) ([]Confirmation, error) {
	rows := []Confirmation{}
	seen := map[string]Confirmation{}
	candidates := []Confirmation{}
	if p.Batch != nil {
		var err error
		candidates, err = checkedBatchConfirmations(*p.Batch, host)
		if err != nil {
			return nil, err
		}
	}
	for _, seg := range segments {
		candidates = append(candidates, seg.rows...)
	}
	for _, row := range candidates {
		if row.BoardLine == "pending" && (p.Batch == nil || row.ID != p.Batch.ID) {
			return nil, invalid("confirmation has no pending transaction")
		}
		key := row.ID + "\x00" + row.AgentID
		if previous, ok := seen[key]; ok {
			if !compatibleConfirmation(previous, row) {
				return nil, invalid("conflicting confirmations")
			}
			continue
		}
		if p.Batch != nil && row.ID == p.Batch.ID && p.Batch.NodeID != "" {
			row.Pending = true
			row.BoardLine = "pending"
		}
		seen[key] = row
		rows = append(rows, row)
	}
	return rows, nil
}

func validatePermissionNoticeOutcome(p permissionPending) error {
	if p.NoticeOutcome != "" && p.NoticeOutcome != "undeliverable" {
		return invalid("permission notice outcome is invalid")
	}
	if p.NoticeOutcome != "" && p.Batch == nil {
		return invalid("permission notice outcome has no batch")
	}

	return nil
}

func validateClearedPermissionPending(p permissionPending) error {
	if p.CompletedID != "" && !permissionID(p.CompletedID) {
		return invalid("permission completed generation is invalid")
	}
	if p.Rotate || p.ActiveHash != "" || p.ArchiveHash != "" || p.BeforeGrants != nil || p.BeforeHash != "" {
		return invalid("empty permission transaction is invalid")
	}
	return nil
}
