// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"encoding/json"
	"os"
	"reflect"
	"time"
)

const maxPermissionReplays = 1024

type permissionReplay struct {
	Batch       PermissionBatch `json:"batch"`
	BatchHash   string          `json:"batch_hash"`
	Cancelled   bool            `json:"cancelled,omitempty"`
	Completed   bool            `json:"completed"`
	CompletedAt time.Time       `json:"completed_at,omitempty"`
}

func checkedPermissionIntentHash(b PermissionBatch, host string) (string, error) {
	type change struct {
		AgentID string `json:"agent_id"`
		Action  string `json:"action"`
	}
	changes := make([]change, 0, len(b.Changes))
	for _, c := range b.Changes {
		changes = append(changes, change{c.AgentID, c.Action})
	}
	markers := append([]string{}, b.Markers...)
	input := struct {
		HostID, ID, BoardID string
		Hub                 HubIdentity
		Mode, Source        string
		Markers             []string
		Changes             []change
	}{host, effectivePermissionCaller(b), b.BoardID, b.Hub, b.Mode, b.Source, markers, changes}
	return permissionRecoveryHash(input)
}
func samePermissionBatch(a, b PermissionBatch) bool {
	if a.Time.Format(time.RFC3339Nano) != b.Time.Format(time.RFC3339Nano) {
		return false
	}
	a.Time, b.Time = a.Time.UTC(), b.Time.UTC()
	return reflect.DeepEqual(a, b)
}

func validateReplayPendingHost(p permissionPending, host string) error {
	if p.Batch == nil || p.Batch.IntentHash == "" {
		return nil
	}
	hash, err := checkedPermissionIntentHash(*p.Batch, host)
	if err != nil {
		return err
	}
	if p.Batch.IntentHash != hash {
		return invalid("permission pending intent hash conflicts")
	}
	return nil
}
func readPermissionReplays(dir *os.File, host string) ([]permissionReplay, error) {
	rows, _, err := readPermissionReplayLedger(dir, host)
	return rows, err
}
func validatePermissionReplays(receipts []permissionReplay, host string) error {
	if receipts == nil || len(receipts) > maxPermissionReplays {
		return invalid("permission replay ledger size is invalid")
	}
	seen := map[string]bool{}
	occurrences := map[string]bool{}
	for _, receipt := range receipts {
		if err := validatePermissionReplayBatch(receipt, host); err != nil {
			return err
		}
		if seen[effectivePermissionCaller(receipt.Batch)] {
			return invalid("duplicate permission replay identity")
		}
		seen[effectivePermissionCaller(receipt.Batch)] = true
		if occurrences[receipt.Batch.ID] {
			return invalid("duplicate permission occurrence identity")
		}
		occurrences[receipt.Batch.ID] = true
		if receipt.Cancelled && !receipt.Completed {
			return invalid("cancelled replay is not terminal")
		}
		if !receipt.Completed && !receipt.CompletedAt.IsZero() {
			return invalid("inflight replay has completion time")
		}
		if !receipt.CompletedAt.IsZero() && (receipt.CompletedAt.Year() < 1 || receipt.CompletedAt.Year() > 9999) {
			return invalid("permission completion time is invalid")
		}
	}
	return nil
}
func validatePermissionReplayBatch(receipt permissionReplay, host string) error {
	if err := validatePermissionBatch(receipt.Batch, true); err != nil {
		return err
	}
	batchHash, err := checkedPermissionBatchHash(receipt.Batch)
	if err != nil {
		return err
	}
	if receipt.BatchHash != batchHash {
		return invalid("permission replay canonical hash conflicts")
	}
	intentHash, err := checkedPermissionIntentHash(receipt.Batch, host)
	if err != nil {
		return err
	}
	if receipt.Batch.IntentHash == "" || receipt.Batch.IntentHash != intentHash {
		return invalid("permission replay intent hash conflicts")
	}
	return nil
}

func encodePermissionReplays(receipts []permissionReplay, host string) ([]byte, error) {
	if len(receipts) > maxPermissionReplays {
		return nil, invalid("permission replay ledger is full")
	}
	data, err := json.Marshal(receipts)
	if err != nil {
		return nil, failure("permission replay encoding", err)
	}
	data, err = json.Marshal(record{HostID: host, Data: data})
	if err != nil {
		return nil, failure("permission replay envelope", err)
	}
	if len(data) > maxRecordSize {
		return nil, invalid("permission replay ledger is full")
	}
	return data, nil
}
func checkPermissionReplayCapacity(dir *os.File, host string, b PermissionBatch) error {
	receipts, err := readPermissionReplays(dir, host)
	if err != nil {
		return err
	}
	hash, err := checkedPermissionBatchHash(b)
	if err != nil {
		return err
	}
	_, err = trimPermissionReplays(append(receipts, permissionReplay{Batch: b, BatchHash: hash}), host, b.ID)
	return err
}
func validatePermissionReplayPending(dir *os.File, host string, p permissionPending) (*permissionReplay, error) {
	if err := validateReplayPendingHost(p, host); err != nil {
		return nil, err
	}
	receipts, err := readPermissionReplays(dir, host)
	if err != nil {
		return nil, err
	}
	for _, receipt := range receipts {
		if !receipt.Completed && (p.Batch == nil || p.Batch.ID != receipt.Batch.ID) {
			return nil, invalid("incomplete replay has no matching pending batch")
		}
	}
	if p.Batch == nil || p.Batch.IntentHash == "" {
		return nil, nil
	}
	for i := range receipts {
		if receipts[i].Batch.ID == p.Batch.ID {
			if !samePermissionBatch(receipts[i].Batch, *p.Batch) {
				return nil, invalid("permission replay pending conflicts")
			}
			return &receipts[i], nil
		}
	}
	return nil, nil
}
func (s *State) writePermissionReplay(tx permissionTransaction, b PermissionBatch, completed bool) error {
	if b.IntentHash == "" {
		return nil
	}
	receipts, err := readPermissionReplays(tx.dir, tx.host)
	if err != nil {
		return err
	}
	found := false
	for i := range receipts {
		if receipts[i].Batch.ID == b.ID {
			if !samePermissionBatch(receipts[i].Batch, b) {
				return invalid("permission replay batch conflicts")
			}
			receipts[i], err = s.completePermissionReplay(receipts[i], completed)
			if err != nil {
				return err
			}
			found = true
			break
		}
	}
	if !found {
		hash, hashErr := checkedPermissionBatchHash(b)
		if hashErr != nil {
			return hashErr
		}
		row, completeErr := s.completePermissionReplay(permissionReplay{Batch: b, BatchHash: hash}, completed)
		if completeErr != nil {
			return completeErr
		}
		receipts = append(receipts, row)
	}
	receipts, err = trimPermissionReplays(receipts, tx.host, b.ID)
	if err != nil {
		return err
	}
	data, err := encodePermissionReplays(receipts, tx.host)
	if err != nil {
		return err
	}
	stage := "permission-replay-accepted"
	if completed {
		stage = "permission-replay-completed"
	}
	return s.commitAlertData(tx.dir, "permissions-replays.json", data, stage)
}

func (s *State) beginPermissionReplay(tx permissionTransaction, p permissionPending) (bool, error) {
	receipt, err := validatePermissionReplayPending(tx.dir, tx.host, p)
	if err != nil {
		return false, err
	}
	if receipt != nil && receipt.Completed {
		return true, s.commitPermissionRecord(tx.dir, "permissions-pending.json", tx.host, clearedPermissionPending(p))
	}
	return false, s.writePermissionReplay(tx, *p.Batch, false)
}
