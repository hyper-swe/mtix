// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
)

// Report describes a record that was ignored during validation.
type Report struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

const maxRecordSize = 1 << 20

type record struct {
	HostID string          `json:"host_id"`
	Data   json.RawMessage `json:"data"`
}

func recordName(name string) (string, error) {
	if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`).MatchString(name) || name == "host-id" {
		return "", invalid("record name is invalid")
	}
	return name + ".json", nil
}

// ReadJSON replaces v with validated host-specific data; absent and ignored records are empty.
// A valid destination is cleared before reading, including when validation fails.
func (s *State) ReadJSON(name string, v any) ([]Report, error) {
	target := reflect.ValueOf(v)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return nil, invalid("destination must be a non-nil pointer")
	}
	target = target.Elem()
	target.SetZero()
	data, report, err := s.readRecord(name)
	if err != nil || data == nil {
		return report, err
	}
	fresh := reflect.New(target.Type())
	if err = json.Unmarshal(data, fresh.Interface()); err != nil {
		return nil, failure("JSON data is invalid", err)
	}
	target.Set(fresh.Elem())
	return report, nil
}

func (s *State) readRecord(name string) (data json.RawMessage, reports []Report, err error) {
	file, err := recordName(name)
	if err != nil {
		return nil, nil, err
	}
	dir, err := s.directory(false)
	if missingState(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer closeWith(dir, &err)
	data, err = readData(dir, file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var rec record
	if err = json.Unmarshal(data, &rec); err != nil {
		return nil, nil, fmt.Errorf("read record %s: %w: %w", name, err, invalid("JSON is invalid"))
	}
	if !validHostID(rec.HostID) || len(rec.Data) == 0 {
		return nil, nil, invalid("record metadata is invalid")
	}
	id, err := s.hostID(dir, false)
	if err != nil {
		return nil, nil, err
	}
	if id != rec.HostID {
		return nil, []Report{{Name: name, Reason: "record belongs to another host"}}, nil
	}
	return rec.Data, nil, nil
}

// WriteJSON durably replaces a host-specific record using a private temporary file.
func (s *State) WriteJSON(name string, v any) (err error) {
	file, err := recordName(name)
	if err != nil {
		return err
	}
	dir, err := s.directory(true)
	if err != nil {
		return err
	}
	defer closeWith(dir, &err)
	id, err := s.hostID(dir, true)
	if err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode record: %w: %w", err, invalid("JSON data is invalid"))
	}
	data, err = json.Marshal(record{HostID: id, Data: data})
	if err != nil {
		return fmt.Errorf("encode envelope: %w", err)
	}
	if len(data) > maxRecordSize {
		return invalid("record is too large")
	}
	return writeData(dir, file, data, false, s.beforeCommit, s.syncDirectory)
}
