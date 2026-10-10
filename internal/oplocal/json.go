// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
)

// Report describes a record that was ignored during validation.
type Report struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

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

// ReadJSON reads a host-specific record; absent and other-host records leave v unchanged.
func (s *State) ReadJSON(name string, v any) (report []Report, err error) {
	file, err := recordName(name)
	if err != nil {
		return nil, err
	}
	dir, err := s.directory(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closeWith(dir, &err)
	data, err := readData(dir, file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec record
	if err = json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("read record %s: %w: %w", name, err, invalid("JSON is invalid"))
	}
	if !validHostID(rec.HostID) || len(rec.Data) == 0 {
		return nil, invalid("record metadata is invalid")
	}
	id, err := s.hostID(dir, false)
	if err != nil {
		return nil, err
	}
	if id != rec.HostID {
		return []Report{{Name: name, Reason: "record belongs to another host"}}, nil
	}
	if err = json.Unmarshal(rec.Data, v); err != nil {
		return nil, fmt.Errorf("decode record %s: %w: %w", name, err, invalid("JSON data is invalid"))
	}
	return nil, nil
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
	return writeData(dir, file, data, false, s.beforeCommit)
}
