// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// relayDirMode is the mode for a relay directory mtix creates.
const relayDirMode = 0o700

// EnsureDir creates the directory root/parts[0]/parts[1]/... one
// component at a time, without ever following a link (FR-21 §5.1).
//
// os.MkdirAll resolves every component through the filesystem, so a
// symlink planted by other software on the shared medium would make it
// create directories at the link's target, outside the relay. Here each
// component is Lstat-checked first: an existing symlink is RELAY_SYMLINK,
// an existing non-directory is RELAY_FOREIGN_ENTRY, and a missing one is
// made with Mkdir and Lstat-checked again, which closes the window in
// which it could be replaced by a link between creation and use. Nothing
// is created beneath a component that has been refused.
//
// root is the operator-configured relay directory. It is followed, not
// Lstat-checked: a root that does not exist yet is created with its
// missing ancestors, at mode 0700, as os.MkdirAll did before, and a root
// that is a symlink (for example to a mounted drive) is used as is. Only
// the components under it are Lstat-checked. It returns the joined path.
func EnsureDir(root string, parts ...string) (string, error) {
	return ensureDirWith(os.Mkdir, root, parts...)
}

// ensureDirWith is EnsureDir with the directory-creating call injected,
// so a test can replace a directory with a link between creation and the
// recheck (FR-21 §5.1). EnsureDir always passes os.Mkdir.
func ensureDirWith(mkdir func(string, os.FileMode) error, root string, parts ...string) (string, error) {
	if err := ensureRoot(root); err != nil {
		return "", err
	}
	dir := root
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\`) {
			return "", fmt.Errorf("%w: %q is not a single path component", ErrForeignEntry, part)
		}
		dir = filepath.Join(dir, part)
		if err := ensureOne(mkdir, dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// ensureOne makes sure dir is a real directory, creating it when absent.
func ensureOne(mkdir func(string, os.FileMode) error, dir string) error {
	err := lstatDir(dir)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if mkErr := mkdir(dir, relayDirMode); mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
		return fmt.Errorf("create %s: %w", dir, mkErr)
	}
	return lstatDir(dir)
}

// ensureRoot makes sure the relay root exists. It is os.MkdirAll, as before
// MTIX-305: a missing root and its ancestors are created at mode 0700, an
// existing symlinked root is followed, and a root that is not a directory
// fails with the error os.MkdirAll returns. Only the components under the
// root are Lstat-checked (FR-21 §5.1).
func ensureRoot(root string) error {
	if err := os.MkdirAll(root, relayDirMode); err != nil {
		return fmt.Errorf("create %s: %w", root, err)
	}
	return nil
}
