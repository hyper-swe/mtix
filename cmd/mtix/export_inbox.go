// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Shell redirection opens (and may truncate) the target before mtix starts.
// SameFile also recognizes a symlink or hard link to the tracked mirror.
func exportIncludesInbox(output *os.File, mtixDir string) (bool, error) {
	target, err := output.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect export output: %w", err)
	}
	mirror, err := os.Stat(filepath.Join(mtixDir, "tasks.json"))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect tracked export mirror: %w", err)
	}
	return !os.SameFile(target, mirror), nil
}

// Explicit recovery input must be readable and validate; a typo must not
// silently fall back to a different source and claim that recovery succeeded.
func checkRecoverExport(path string) (err error) {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open recovery export: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	data, err := sqlite.DecodeExportData(file)
	if err != nil {
		return fmt.Errorf("read recovery export: %w", err)
	}
	if err := sqlite.ValidateExport(data); err != nil {
		return fmt.Errorf("validate recovery export: %w", err)
	}
	return nil
}
