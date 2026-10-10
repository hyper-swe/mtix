// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Status reports operator state availability without creating files.
type Status struct {
	Path    string   `json:"path"`
	Status  string   `json:"status"`
	Reports []Report `json:"reports"`
	Detail  string   `json:"detail,omitempty"`
	Fix     string   `json:"fix,omitempty"`
}

// Inspect validates existing state and reports records without making grants.
func (s *State) Inspect() (status Status, err error) {
	status.Reports = []Report{}
	status.Path, err = s.resolve()
	if err != nil {
		return status, err
	}
	dir, err := s.directory(false)
	if errors.Is(err, os.ErrNotExist) {
		status.Status = "absent"
		return status, nil
	}
	if err != nil {
		return status, err
	}
	defer closeWith(dir, &err)
	status.Status = "ready"
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return status, fmt.Errorf("list state: %w: %w", err, invalid("read failed"))
	}
	for _, name := range names {
		if name == "host-id" {
			if _, err = s.hostID(dir, false); err != nil {
				return status, err
			}
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		var value any
		reports, readErr := s.ReadJSON(strings.TrimSuffix(name, ".json"), &value)
		if readErr != nil {
			return status, readErr
		}
		status.Reports = append(status.Reports, reports...)
	}
	return status, nil
}
