// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"errors"
	"fmt"
	"os"
)

func readRegularDoc(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file; refusing to write", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return content, true, nil
}

// writeRegularDoc refuses links and nonregular files before any truncation.
func writeRegularDoc(path string, content []byte) (err error) {
	info, statErr := os.Lstat(path)
	flags := os.O_RDWR
	switch {
	case os.IsNotExist(statErr):
		flags |= os.O_CREATE | os.O_EXCL
		info = nil
	case statErr != nil:
		return fmt.Errorf("inspect %s: %w", path, statErr)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file; refusing to write", path)
	}
	file, err := os.OpenFile(path, flags, 0644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened %s: %w", path, err)
	}
	if !opened.Mode().IsRegular() || (info != nil && !os.SameFile(info, opened)) {
		return fmt.Errorf("%s changed while opening; refusing to write", path)
	}
	if truncateErr := file.Truncate(0); truncateErr != nil {
		return fmt.Errorf("truncate %s: %w", path, truncateErr)
	}
	if _, writeErr := file.Write(content); writeErr != nil {
		return fmt.Errorf("write %s: %w", path, writeErr)
	}
	return nil
}
