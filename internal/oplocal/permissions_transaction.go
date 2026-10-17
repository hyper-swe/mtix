// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"encoding/json"
	"maps"
	"os"
)

type permissionTransaction struct {
	ctx     context.Context
	dir     *os.File
	host    string
	publish func(context.Context, PermissionBatch) error
	boardID string
}

func (s *State) acceptPermissions(tx permissionTransaction, b PermissionBatch) error {
	dir, host, publish := tx.dir, tx.host, tx.publish
	if b.NodeID != "" && publish == nil {
		return invalid("permission publisher is required")
	}
	segments, err := readConfirmationSegments(dir, host)
	if err != nil {
		return err
	}
	if _, err = logicalConfirmations(segments, permissionPending{}, host); err != nil {
		return err
	}
	rows, err := checkedBatchConfirmations(b, host)
	if err != nil {
		return err
	}
	exists, err := confirmationBatchExists(segments, rows)
	if err != nil || exists {
		return err
	}
	data, grants, err := readPermissionGrants(dir, host)
	if err != nil {
		return err
	}
	before, beforeHash, err := capturePermissionBefore(grants, b)
	if err != nil {
		return err
	}
	if _, err = permissionGrantProjection(data, grants, b, host); err != nil {
		return err
	}
	if _, err = encodeConfirmations(rows); err != nil {
		return err
	}
	active := append(append([]Confirmation{}, segments[0].rows...), rows...)
	_, overflow := encodeConfirmations(active)
	generation, err := newPermissionGeneration()
	if err != nil {
		return err
	}
	pending := permissionPending{Generation: generation, Batch: &b, Rotate: overflow != nil, ActiveHash: alertHash(segments[0].data), ArchiveHash: alertHash(segments[1].data), BeforeGrants: &before, BeforeHash: beforeHash}
	if operationErr := s.commitPermissionRecord(dir, "permissions-pending.json", host, pending); operationErr != nil {
		return operationErr
	}
	return s.finishPermissions(tx, pending)
}
func confirmationBatchExists(segments [3]confirmationSegment, rows []Confirmation) (bool, error) {
	stored := map[string]Confirmation{}
	for _, segment := range segments {
		for _, row := range segment.rows {
			if row.ID == rows[0].ID {
				if previous, ok := stored[row.AgentID]; ok && !compatibleConfirmation(previous, row) {
					return false, invalid("conflicting confirmations")
				}
				stored[row.AgentID] = row
			}
		}
	}
	if len(stored) == 0 {
		return false, nil
	}
	if len(stored) != len(rows) {
		return false, invalid("permission batch identity conflicts")
	}
	for _, row := range rows {
		if !compatibleConfirmation(stored[row.AgentID], row) {
			return false, invalid("permission batch identity conflicts")
		}
	}
	return true, nil
}
func (s *State) finishPermissions(tx permissionTransaction, p permissionPending) error {
	if handled, err := s.recoverPermissionCancellation(tx, p); handled {
		return err
	}
	dir, host := tx.dir, tx.host
	p, err := s.preparePermissionFinish(tx, p)
	if err != nil {
		return err
	}
	complete, err := s.beginPermissionReplay(tx, p)
	if err != nil || complete {
		return err
	}
	segments, err := readConfirmationSegments(dir, host)
	if err != nil {
		return err
	}
	if _, err = logicalConfirmations(segments, p, host); err != nil {
		return err
	}
	if operationErr := s.projectConfirmations(dir, host, p, segments); operationErr != nil {
		return operationErr
	}
	data, grants, err := readPermissionGrants(dir, host)
	if err != nil {
		return err
	}
	projected, err := permissionGrantProjection(data, grants, *p.Batch, host)
	if err != nil {
		return err
	}
	if operationErr := s.commitPermissionRecord(dir, "grants.json", host, projected); operationErr != nil {
		return operationErr
	}
	p, err = s.publishPermissionNotice(tx, p)
	if err != nil {
		return err
	}
	if operationErr := s.finalizePermissionNotice(tx, p); operationErr != nil {
		return operationErr
	}
	if operationErr := s.writePermissionReplay(tx, *p.Batch, true); operationErr != nil {
		return operationErr
	}
	return s.commitPermissionRecord(dir, "permissions-pending.json", host, clearedPermissionPending(p))
}
func permissionGrantProjection(data map[string]json.RawMessage, grants []AgentGrant, b PermissionBatch, host string) (map[string]json.RawMessage, error) {
	data = maps.Clone(data)
	grants = append([]AgentGrant{}, grants...)
	for _, change := range b.Changes {
		if change.Outcome != "" {
			continue
		}
		next := AgentGrant{HostID: host, BoardID: b.BoardID, Hub: b.Hub, AgentID: change.AgentID, ConfirmedAt: b.Time, Mode: b.Mode, Source: b.Source, Denied: change.Action == "deny", PendingRegistration: change.PendingRegistration}
		found := false
		for i, old := range grants {
			if old.BoardID == b.BoardID && old.AgentID == change.AgentID {
				grants[i] = next
				found = true
				break
			}
		}
		if !found {
			grants = append(grants, next)
		}
	}
	encoded, err := json.Marshal(grants)
	if err != nil {
		return nil, failure("grant projection", err)
	}
	data["agent_grants"] = encoded
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, failure("grant data", err)
	}
	encoded, err = json.Marshal(record{HostID: host, Data: payload})
	if err != nil {
		return nil, failure("grant envelope", err)
	}
	if len(encoded) > maxRecordSize {
		return nil, invalid("grant projection exceeds limit")
	}
	return data, nil
}
