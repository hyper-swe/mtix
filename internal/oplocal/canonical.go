// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

type locationError struct{ err error }

func (e *locationError) Error() string { return e.err.Error() }
func (e *locationError) Unwrap() error { return e.err }

func locationFailure(err error) error {
	return &locationError{err: failure("configured path metadata is unavailable", err)}
}

func missingState(err error) bool {
	var location *locationError
	return errors.Is(err, os.ErrNotExist) && !errors.As(err, &location)
}

func canonicalLocation(input string) (string, error) {
	if err := validateCanonicalInput(input); err != nil {
		return "", err
	}
	current, err := filepath.Abs(input)
	if err != nil {
		return "", locationFailure(err)
	}
	var suffix []string
	for {
		_, err = os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", locationFailure(err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", locationFailure(err)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
	info, err := os.Stat(current)
	if err != nil {
		return "", locationFailure(err)
	}
	if !info.IsDir() {
		return "", locationFailure(&os.PathError{Op: "stat", Path: current, Err: syscall.ENOTDIR})
	}
	canonical, err := canonicalExistingPath(current)
	if err != nil {
		return "", locationFailure(err)
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		canonical = filepath.Join(canonical, suffix[i])
	}
	return canonical, nil
}

func validateCanonicalInput(input string) error {
	return validatePlatformInput(runtime.GOOS, input)
}

func cleanAbsolute(path string) bool { return filepath.IsAbs(path) && filepath.Clean(path) == path }

// lanePaths verifies repository membership from bounded metadata reads. It
// never executes repository commands or trusts caller-supplied membership.
func lanePaths(loc LaneLocation) (root, relative, common string, main bool, err error) {
	if !laneHarness.MatchString(loc.Harness) || !cleanAbsolute(loc.Root) || !cleanAbsolute(loc.Path) {
		err = invalid("lane location is invalid")
		return
	}
	if loc.BoardKey != "" && !validRoutingBoard(loc.BoardKey) {
		err = invalid("lane board identity is invalid")
		return
	}
	root, err = filepath.EvalSymlinks(loc.Root)
	if err != nil {
		return
	}
	path, pathErr := filepath.EvalSymlinks(loc.Path)
	if pathErr != nil {
		err = pathErr
		return
	}
	relative, err = filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		err = invalid("lane path is outside its board")
		return
	}
	common, main, err = laneRepository(root)
	return
}

func laneMetadata(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read lane metadata: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", invalid("lane metadata must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open lane metadata: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect opened lane metadata: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() > 4096 {
		return "", invalid("lane metadata changed during open")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", fmt.Errorf("read lane metadata: %w", err)
	}
	if len(raw) > 4096 {
		return "", invalid("lane metadata grew beyond its limit")
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" || strings.ContainsAny(text, "\n\r\x00") {
		return "", invalid("lane metadata has an invalid path")
	}
	return text, nil
}

func laneRepository(root string) (string, bool, error) {
	dot := filepath.Join(root, ".git")
	info, err := os.Lstat(dot)
	if errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect lane repository: %w", err)
	}
	if info.IsDir() {
		return dot, true, nil
	}
	line, err := laneMetadata(dot)
	if err != nil {
		return "", false, err
	}
	if !strings.HasPrefix(line, "gitdir: ") {
		return "", false, invalid("lane gitdir marker is invalid")
	}
	admin, err := laneMetadataPath(root, strings.TrimPrefix(line, "gitdir: "))
	if err != nil {
		return "", false, err
	}
	return verifyLaneWorktree(root, admin)
}

func laneMetadataPath(base, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve lane repository path: %w", err)
	}
	return filepath.Clean(canonical), nil
}

func verifyLaneWorktree(root, admin string) (string, bool, error) {
	raw, err := laneMetadata(filepath.Join(admin, "commondir"))
	if err != nil {
		return "", false, err
	}
	common, err := laneMetadataPath(admin, raw)
	if err != nil {
		return "", false, err
	}
	// A matching backlink alone can be forged in an arbitrary directory. The
	// admin directory must also be a direct child of the common worktrees dir.
	if filepath.Dir(admin) != filepath.Join(common, "worktrees") {
		return "", false, invalid("lane administration directory is outside its common directory")
	}
	raw, err = laneMetadata(filepath.Join(admin, "gitdir"))
	if err != nil {
		return "", false, err
	}
	back, err := laneMetadataPath(admin, raw)
	if err != nil {
		return "", false, err
	}
	if back != filepath.Join(root, ".git") {
		return "", false, invalid("lane worktree backlink does not match")
	}
	info, err := os.Stat(common)
	if err != nil {
		return "", false, fmt.Errorf("inspect lane common directory: %w", err)
	}
	if !info.IsDir() {
		return "", false, invalid("lane common directory is not a directory")
	}
	return common, false, nil
}

// ValidateLaneLocation checks paths and worktree membership without writing state.
func ValidateLaneLocation(loc LaneLocation) error { _, _, _, _, err := lanePaths(loc); return err }
