// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import "os"

// Unix records and release-defined executables retain their established modes.
const (
	recordMode     = 0600
	executableMode = 0700
)

type writeOptions struct {
	exclusive     bool
	before        func() error
	syncDirectory func(*os.File) error
	mode          uint32
	flushNative   func(*os.File) error
}

func (s *State) writeData(dir *os.File, name string, data []byte, exclusive bool) error {
	opts := writeOptions{exclusive: exclusive, before: s.beforeCommit, syncDirectory: s.syncDirectory, mode: recordMode, flushNative: s.flushNative}
	return writeDataMode(dir, name, data, opts)
}
