// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"time"
)

type confirmationSegment struct {
	data []byte
	rows []Confirmation
}
type permissionPending struct {
	Generation    string           `json:"generation,omitempty"`
	CompletedID   string           `json:"completed_id,omitempty"`
	Batch         *PermissionBatch `json:"batch"`
	Rotate        bool             `json:"rotate"`
	ActiveHash    string           `json:"active_hash"`
	ArchiveHash   string           `json:"archive_hash"`
	BeforeGrants  *[]AgentGrant    `json:"before_agent_grants,omitempty"`
	BeforeHash    string           `json:"before_hash,omitempty"`
	NoticeOutcome string           `json:"notice_outcome,omitempty"`
}

func confirmationFilename(index int) string {
	if index == 0 {
		return "confirmations.jsonl"
	}
	if index == 1 {
		return "confirmations.1.jsonl"
	}
	return "confirmations.2.jsonl"
}
func sameConfirmation(a, b Confirmation) bool {
	if a.Time.Format(time.RFC3339Nano) != b.Time.Format(time.RFC3339Nano) {
		return false
	}
	a.Time, b.Time = a.Time.UTC(), b.Time.UTC()
	a.Pending, b.Pending = false, false
	return reflect.DeepEqual(a, b)
}

func readConfirmationSegments(dir *os.File, host string) ([3]confirmationSegment, error) {
	var segments [3]confirmationSegment
	for i := range segments {
		data, err := readData(dir, confirmationFilename(i))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return segments, err
		}
		rows, err := decodeConfirmations(data, host)
		if err != nil {
			return segments, err
		}
		segments[i] = confirmationSegment{data: data, rows: rows}
	}
	return segments, nil
}
func decodeConfirmations(data []byte, host string) ([]Confirmation, error) {
	rows := []Confirmation{}
	if len(data) == 0 {
		return rows, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, invalid("confirmation log is incomplete")
	}
	for _, line := range bytes.Split(data[:len(data)-1], []byte{'\n'}) {
		var row Confirmation
		if err := decodeAlertJSON(line, &row); err != nil {
			return nil, err
		}
		if err := validateConfirmation(row, host); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func encodeConfirmations(rows []Confirmation) ([]byte, error) {
	data := []byte{}
	reserve := 0
	for _, row := range rows {
		if row.BoardLine == "pending" {
			reserve += len("undeliverable") - len("pending")
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, failure("confirmation encoding", err)
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if len(data)+reserve > maxRecordSize {
		return nil, invalid("confirmation segment exceeds limit")
	}
	return data, nil
}
func readPermissionGrants(dir *os.File, host string) (map[string]json.RawMessage, []AgentGrant, error) {
	data := map[string]json.RawMessage{}
	raw, err := readData(dir, "grants.json")
	if errors.Is(err, os.ErrNotExist) {
		return data, []AgentGrant{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var envelope record
	if operationErr := decodeAlertJSON(raw, &envelope); operationErr != nil {
		return nil, nil, operationErr
	}
	if envelope.HostID != host {
		return nil, nil, invalid("grants belong to another host")
	}
	if operationErr := decodeAlertJSON(envelope.Data, &data); operationErr != nil {
		return nil, nil, operationErr
	}
	if data == nil {
		return nil, nil, invalid("grant record must be an object")
	}
	if operationErr := validatePermissionBoardGrants(data["board_grants"]); operationErr != nil {
		return nil, nil, operationErr
	}
	grants := []AgentGrant{}
	if raw, ok := data["agent_grants"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, nil, invalid("agent grants must be an array")
		}
		if operationErr := decodeAlertJSON(raw, &grants); operationErr != nil {
			return nil, nil, operationErr
		}
	}
	seen := map[string]bool{}
	for _, grant := range grants {
		if operationErr := validateAgentGrant(grant, host); operationErr != nil {
			return nil, nil, operationErr
		}
		key := grant.BoardID + "\x00" + grant.AgentID
		if seen[key] {
			return nil, nil, invalid("duplicate agent grant")
		}
		seen[key] = true
	}
	return data, grants, nil
}
func checkedBatchConfirmations(b PermissionBatch, host string) ([]Confirmation, error) {
	hash, err := checkedPermissionBatchHash(b)
	if err != nil {
		return nil, err
	}
	rows := make([]Confirmation, 0, len(b.Changes))
	line := "pending"
	if b.NodeID == "" {
		line = "skipped_no_node"
	}
	for _, change := range b.Changes {
		rows = append(rows, Confirmation{ID: b.ID, CallerID: b.CallerID, Outcome: change.Outcome, BatchHash: hash, HostID: host, BoardID: b.BoardID, AgentID: change.AgentID, Action: change.Action, PendingRegistration: change.PendingRegistration, Time: b.Time, Mode: b.Mode, Source: b.Source, Markers: b.Markers, BoardLine: line})
	}
	return rows, nil
}
func (s *State) commitPermissionRecord(dir *os.File, name, host string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return failure("permission encoding", err)
	}
	data, err = json.Marshal(record{HostID: host, Data: data})
	if err != nil {
		return failure("permission envelope", err)
	}
	if len(data) > maxRecordSize {
		return invalid("permission record exceeds limit")
	}
	stage := name
	if pending, ok := value.(permissionPending); ok && pending.Batch == nil {
		stage = "permissions-clear"
	}
	return s.commitAlertData(dir, name, data, stage)
}

func validatePermissionBoardGrants(data json.RawMessage) error {
	if len(data) == 0 {
		return nil
	}
	var grants []Grant
	if err := decodeAlertJSON(data, &grants); err != nil {
		return err
	}
	for _, grant := range grants {
		if !validBoardID(grant.BoardID) {
			return invalid("recorded board identity is invalid")
		}
		if err := validateHub(grant.Hub); err != nil {
			return err
		}
	}
	return nil
}

func checkedPermissionBatchHash(batch PermissionBatch) (string, error) {
	return permissionRecoveryHash(batch)
}
