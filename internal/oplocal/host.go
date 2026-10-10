// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

func validHostID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && strings.ToLower(id) == id
}

// HostID reads or creates this directory's persistent host identifier.
func (s *State) HostID() (id string, err error) {
	dir, err := s.directory(true)
	if err != nil {
		return "", err
	}
	defer closeWith(dir, &err)
	return s.hostID(dir, true)
}

func (s *State) hostID(dir *os.File, create bool) (string, error) {
	data, err := readData(dir, "host-id")
	if errors.Is(err, os.ErrNotExist) && create {
		b := make([]byte, 16)
		if _, err = rand.Read(b); err != nil {
			return "", failure("host identifier generation failed", err)
		}
		id := hex.EncodeToString(b)
		err = writeData(dir, "host-id", []byte(id+"\n"), true, nil)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		data, err = readData(dir, "host-id")
	}
	if err != nil {
		return "", failure("host identifier is unavailable", err)
	}
	id := strings.TrimSpace(string(data))
	if !validHostID(id) {
		return "", invalid("host identifier is invalid")
	}
	return id, nil
}
