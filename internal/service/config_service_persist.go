// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// writeFileAtomic writes data to a temp file beside path and renames it over
// path, so a process re-reading the config (HistoryExport) never sees a
// truncated file. It keeps the mode of the file it replaces (0644 for a new
// one), writes through a symlink (the link target is replaced, the link is
// kept), and syncs the file and its directory before and after the rename.
func writeFileAtomic(path string, data []byte) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	name := tmp.Name()
	fail := func(step string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("%s: %w", step, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail("write temp config", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail("chmod temp config", err)
	}
	if err := tmp.Sync(); err != nil {
		return fail("sync temp config", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("replace config: %w", err)
	}
	return syncDir(dir)
}

// syncDir flushes a directory entry change to disk.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is the config file's own directory
	if err != nil {
		return fmt.Errorf("open config dir: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("sync config dir: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close config dir: %w", err)
	}
	return nil
}

// parseDuration parses a duration string with support for "d" (days) suffix.
func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	// Handle "30d" format.
	if strings.HasSuffix(s, "d") {
		s = strings.TrimSuffix(s, "d")
		days := 0
		for _, c := range s {
			if c < '0' || c > '9' {
				return fallback
			}
			days = days*10 + int(c-'0')
		}
		return time.Duration(days) * 24 * time.Hour
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
