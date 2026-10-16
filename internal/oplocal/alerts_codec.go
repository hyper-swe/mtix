// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

type alertSegment struct {
	data []byte
	rows []Alert
}
type alertPending struct {
	Row                  *Alert                  `json:"row"`
	PermissionOccurrence *permissionAlertPending `json:"permission_occurrence,omitempty"`
	Kind                 string                  `json:"kind"`
	Target               int                     `json:"target"`
	ActiveHash           string                  `json:"active_hash"`
	ArchiveHash          string                  `json:"archive_hash"`
	TargetHash           string                  `json:"target_hash"`
}

func alertFilename(index int) string {
	switch index {
	case 0:
		return "alerts.jsonl"
	case 1:
		return "alerts.1.jsonl"
	default:
		return "alerts.2.jsonl"
	}
}
func alertHash(data []byte) string {
	if data == nil {
		return "absent"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func alertRateName(agent, class string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + class))
	return "alert-rate-" + hex.EncodeToString(sum[:16]) + ".json"
}
func decodeAlertJSON(data []byte, out any) error {
	if err := validateAlertJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return invalid("alert JSON is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return invalid("alert JSON has trailing content")
	}
	return nil
}
func readAlertSegments(dir *os.File, host string) ([3]alertSegment, error) {
	var segments [3]alertSegment
	for i := range segments {
		data, err := readData(dir, alertFilename(i))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return segments, err
		}
		rows, err := decodeAlertSegment(data, host)
		if err != nil {
			return segments, err
		}
		segments[i] = alertSegment{data: data, rows: rows}
	}
	return segments, nil
}
func decodeAlertSegment(data []byte, host string) ([]Alert, error) {
	if len(data) == 0 {
		return []Alert{}, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, invalid("alert log is incomplete")
	}
	var rows []Alert
	for _, line := range bytes.Split(data[:len(data)-1], []byte{'\n'}) {
		var row Alert
		if err := decodeAlertJSON(line, &row); err != nil {
			return nil, err
		}
		if err := validateStoredAlert(row, host); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func encodeAlertSegment(rows []Alert) ([]byte, error) {
	var result []byte
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			return nil, failure("alert encoding failed", err)
		}
		result = append(result, data...)
		result = append(result, '\n')
	}
	if len(result) > maxRecordSize {
		return nil, invalid("alert segment exceeds limit")
	}
	return result, nil
}
func readAlertRecord(dir *os.File, name, host string, out any) (bool, error) {
	data, err := readData(dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var envelope record
	if validationErr := decodeAlertJSON(data, &envelope); validationErr != nil {
		return false, validationErr
	}
	if envelope.HostID != host || len(envelope.Data) == 0 {
		return false, invalid("alert record belongs to another host or is invalid")
	}
	if validationErr := decodeAlertJSON(envelope.Data, out); validationErr != nil {
		return false, validationErr
	}
	return true, nil
}
func (s *State) writeAlertRecord(dir *os.File, name, host string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return failure("alert encoding failed", err)
	}
	data, err = json.Marshal(record{HostID: host, Data: data})
	if err != nil {
		return failure("alert envelope failed", err)
	}
	if len(data) > maxRecordSize {
		return invalid("alert record exceeds limit")
	}
	stage := name
	if strings.HasPrefix(name, "alert-rate-") {
		stage = "rate"
	}
	if p, ok := value.(alertPending); ok && p.Row == nil {
		stage = "clear"
	}
	return s.commitAlertData(dir, name, data, stage)
}

func (s *State) commitAlertData(dir *os.File, name string, data []byte, stage string) error {
	if s.alertStage != nil {
		if err := s.alertStage("before:" + stage); err != nil {
			return err
		}
	}
	if err := writeData(dir, name, data, false, s.beforeCommit, s.syncDirectory); err != nil {
		return err
	}
	if s.alertStage != nil {
		return s.alertStage("after:" + stage)
	}
	return nil
}
