// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hyper-swe/mtix/internal/model"
)

const preserveDatabaseSidecars = "keep the database and its -wal and -shm together: the WAL may hold the only copy of committed records, and SQLite treats such a file as empty and deletes its WAL when it opens it. To restore into this project, move all three files together out of the project (or the entire data/ directory); a copy leaves the refused files in place, so restoring here stays refused, and a copy is enough only for restoring into a fresh project. Do not move only the database"

// maxLinkHops bounds the walk along database and directory links. SQLite
// itself follows up to 200; a walk that runs past this cap fails closed.
const maxLinkHops = 255

// splitPathParts splits a path into its volume and its non-empty, non-"."
// components, keeping ".." for the walk to apply after any link.
func splitPathParts(path string) (string, []string) {
	volume := filepath.VolumeName(path)
	var parts []string
	for _, part := range strings.Split(filepath.ToSlash(path[len(volume):]), "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return volume, parts
}

// resolveDatabasePath returns the file SQLite will really open for path,
// walking it the way the kernel does (NFR-2.8): component by component, with
// ".." applied to the directory a link led to, never to the text before it.
// A dangling final link is followed to the path it would create. Where a
// component does not exist (or is not a directory) the rest is taken as written, since nothing can
// link below a missing directory. It returns an error when it cannot finish
// (a link loop, a chain past maxLinkHops, an unreadable component), and the
// caller must then refuse rather than guess.
func resolveDatabasePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("working directory: %w", err)
		}
		path = wd + string(filepath.Separator) + path
	}
	volume, queue := splitPathParts(path)
	root := volume + string(filepath.Separator)
	resolved := root
	for hops := 0; len(queue) > 0; {
		part := queue[0]
		queue = queue[1:]
		if part == ".." {
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, part)
		info, err := os.Lstat(next)
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return filepath.Join(append([]string{next}, queue...)...), nil
		}
		if err != nil {
			return "", fmt.Errorf("examine %s: %w", next, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		if hops++; hops > maxLinkHops {
			return "", fmt.Errorf("more than %d links on the way to %s", maxLinkHops, path)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", fmt.Errorf("read link %s: %w", next, err)
		}
		targetVolume, targetParts := splitPathParts(target)
		if filepath.IsAbs(target) {
			resolved = targetVolume + string(filepath.Separator)
		}
		queue = append(targetParts, queue...)
	}
	return resolved, nil
}

// guardEmptyDatabaseWAL must precede every SQLite open (openSQLite calls it,
// Store.New calls it before the integrity escape hatch, and Backup calls it
// on its destination). SQLite can discard the WAL of an empty or one-byte
// database. A missing main file beside a WAL, including a dangling link whose
// target or link has one, is equally unsafe; a fresh store has no WAL. When
// the path cannot be walked to its end the guard refuses: it never answers
// "no WAL here" from a path it worked out differently from the kernel.
func guardEmptyDatabaseWAL(path string) error {
	resolved, err := resolveDatabasePath(path)
	if err != nil {
		return fmt.Errorf("refusing to open %s: cannot tell which file SQLite would open: %w: %w; run 'mtix recover'; %s", path, model.ErrCorrupted, err, preserveDatabaseSidecars)
	}
	for _, candidate := range []string{resolved, path} {
		if err := guardWALBeside(candidate, resolved); err != nil {
			return err
		}
	}
	return nil
}

// guardWALBeside refuses when a nonempty WAL sits beside candidate while the
// database SQLite will open (resolved) is empty, one byte or missing.
func guardWALBeside(candidate, resolved string) error {
	walPath := candidate + "-wal"
	wal, err := os.Stat(walPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat WAL before open %s: %w", walPath, err)
	}
	if wal.Size() == 0 {
		return nil
	}
	// Observe the WAL first, then the database: a concurrent first opener may
	// initialize the database before committing its first WAL transaction.
	info, err := os.Stat(resolved)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat database before open: %w", err)
	}
	if info != nil && info.Size() > 1 {
		return nil
	}
	return fmt.Errorf("refusing to open empty, one-byte or missing database %s beside nonempty WAL %s: %w; run 'mtix recover'; %s", resolved, walPath, model.ErrCorrupted, preserveDatabaseSidecars)
}
