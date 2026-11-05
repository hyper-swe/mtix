// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// swap moves the store aside and seeds a fresh one; every failure from the move
// on restores the original (ADR-007 section 4.2 steps 2-5).
func (s *WorktreeEnrolment) swap(ctx context.Context, root string, r EnrollmentRequest, snap *WorktreeSnapshot, res *WorktreeEnrolResult) error {
	data := filepath.Join(root, ".mtix", "data")
	if _, err := os.Lstat(res.AsideDir); err == nil {
		return fmt.Errorf("%s already exists: %w", res.AsideDir, model.ErrConflict)
	}
	if err := s.o.Rename(data, res.AsideDir); err != nil {
		return fmt.Errorf("move store aside: %w", err)
	}
	if err := s.seed(ctx, root, r, snap); err != nil {
		return s.rollback(data, res, err)
	}
	return nil
}

func (s *WorktreeEnrolment) seed(ctx context.Context, root string, r EnrollmentRequest, snap *WorktreeSnapshot) error {
	p := s.p()
	if err := p.Clone(ctx, root); err != nil {
		return fmt.Errorf("clone fresh store from the hub: %w", err)
	}
	if err := p.ImportCarry(ctx, root, snap.Carry, s.o.Clock().UTC()); err != nil {
		return fmt.Errorf("carry inbox state: %w", err)
	}
	if err := p.Enroll(ctx, r); err != nil {
		return fmt.Errorf("enroll worktree: %w", err)
	}
	return nil
}

// rollback removes the half-built store and puts the original back. If the
// restore itself fails, the original is intact in the aside directory and the
// error names it.
func (s *WorktreeEnrolment) rollback(data string, res *WorktreeEnrolResult, cause error) error {
	if _, err := os.Lstat(data); err == nil {
		if rmErr := s.o.RemoveTree(data); rmErr != nil {
			return errors.Join(cause, fmt.Errorf("remove partial store %s: %w; original store is intact in %s, backup %s", data, rmErr, res.AsideDir, res.BackupPath))
		}
	}
	if mvErr := s.o.Rename(res.AsideDir, data); mvErr != nil {
		return errors.Join(cause, fmt.Errorf("restore store: %w; original store is intact in %s, backup %s", mvErr, res.AsideDir, res.BackupPath))
	}
	return fmt.Errorf("%w (the original store was restored; backup %s)", cause, res.BackupPath)
}

// removeTreeFilesFirst deletes files, then the emptied directories deepest
// first, from a path that must be a ".mtix/data" directory.
func removeTreeFilesFirst(path string) error {
	if filepath.Base(path) != "data" || filepath.Base(filepath.Dir(path)) != ".mtix" {
		return fmt.Errorf("refusing to remove %s: not a .mtix/data directory: %w", path, model.ErrInvalidInput)
	}
	var files, dirs []string
	// path was checked above to be a .mtix/data directory.
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error { //nolint:gosec // G703: path is validated above to be a .mtix/data directory
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		} else {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("list %s: %w", path, err)
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			return fmt.Errorf("remove file %s: %w", f, err)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		if !strings.HasPrefix(d, path) {
			return fmt.Errorf("directory %s outside %s: %w", d, path, model.ErrInvalidInput)
		}
		if err := os.Remove(d); err != nil {
			return fmt.Errorf("remove directory %s: %w", d, err)
		}
	}
	return nil
}
