// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// memoryStore is an in-memory operator store for unit tests; real operator
// state is exercised by the oplocal and cmd/mtix tests.
type memoryStore struct {
	dir      string
	records  map[string][]byte
	execs    map[string][]byte
	writeErr error
	reports  []oplocal.Report
}

func newMemoryStore(dir string) *memoryStore {
	return &memoryStore{dir: dir, records: map[string][]byte{}, execs: map[string][]byte{}}
}

func (m *memoryStore) ReadJSON(name string, v any) ([]oplocal.Report, error) {
	target := reflect.ValueOf(v).Elem()
	target.SetZero()
	data, ok := m.records[name]
	if !ok || len(m.reports) > 0 {
		return m.reports, nil
	}
	return nil, json.Unmarshal(data, v)
}

func (m *memoryStore) WriteJSON(name string, v any) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.records[name] = data
	return nil
}

func (m *memoryStore) Path() (string, error) { return m.dir, nil }

func (m *memoryStore) WriteExecutable(name string, data []byte) (string, error) {
	if m.writeErr != nil {
		return "", m.writeErr
	}
	m.execs[name] = append([]byte(nil), data...)
	return filepath.Join(m.dir, name), nil
}

func (m *memoryStore) ReadExecutable(name string) ([]byte, string, error) {
	data, ok := m.execs[name]
	if !ok {
		return nil, filepath.Join(m.dir, name), os.ErrNotExist
	}
	return data, filepath.Join(m.dir, name), nil
}

var errSandboxed = errors.New("operator state is read-only here")
