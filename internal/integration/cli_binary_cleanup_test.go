// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package lifetime owns the shared CLI binary, including a failed build's parent.
package integration

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps the binary until every test has finished and preserves m.Run's
// exact exit code even if cleanup is refused. The assigned build path owns only
// its newly allocated parent; historical same-prefix directories are not scanned.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := cleanupBuiltBinary(mtixBinaryPath); err != nil {
		fmt.Fprintf(os.Stderr, "clean up shared CLI binary: %v\n", err)
	}
	os.Exit(code)
}

func cleanupBuiltBinary(binary string) error {
	if binary == "" {
		return nil
	}
	root, err := ownedBinaryDirectory(binary)
	if err != nil {
		return err
	}
	files, dirs, err := inventoryBinaryDirectory(root)
	if err != nil {
		return err
	}
	for _, path := range files {
		if err := removeBinaryEntry(path, false); err != nil {
			return err
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := removeBinaryEntry(dirs[i], true); err != nil {
			return err
		}
	}
	return nil
}

func ownedBinaryDirectory(binary string) (string, error) {
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary || filepath.Base(binary) != "mtix" {
		return "", fmt.Errorf("refuse malformed owned binary path %q", binary)
	}
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("resolve temp root: %w", err)
	}
	trusted, err := filepath.EvalSymlinks(temp)
	if err != nil {
		return "", fmt.Errorf("resolve trusted temp root: %w", err)
	}
	parent := filepath.Dir(binary)
	// Only the trusted root may be a platform symlink (/var on macOS).
	if filepath.Dir(parent) != temp && filepath.Dir(parent) != trusted {
		return "", fmt.Errorf("refuse binary outside direct temp child: %q", binary)
	}
	name := filepath.Base(parent)
	if !strings.HasPrefix(name, "mtix-cli-test-") || name == "mtix-cli-test-" {
		return "", fmt.Errorf("refuse unowned binary parent %q", parent)
	}
	return filepath.Join(trusted, name), nil
}

// Validate the COMPLETE inventory before removing even one regular file.
// This private build directory has no concurrent owner; this is not a general
// hostile-concurrency deletion API. Symlinks and special entries are refused.
func inventoryBinaryDirectory(root string) ([]string, []string, error) {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("inspect binary parent: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("refuse non-directory binary parent %q", root)
	}
	var files, dirs []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inventory binary entry: %w", walkErr)
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return fmt.Errorf("inspect binary entry: %w", statErr)
		}
		switch {
		case info.IsDir():
			dirs = append(dirs, path)
		case info.Mode().IsRegular():
			files = append(files, path)
		default:
			return fmt.Errorf("refuse symlink or special binary entry %q", path)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("inventory owned binary directory: %w", err)
	}
	return files, dirs, nil
}

func removeBinaryEntry(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("recheck binary entry: %w", err)
	}
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("refuse changed binary entry %q", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove binary entry: %w", err)
	}
	return nil
}
