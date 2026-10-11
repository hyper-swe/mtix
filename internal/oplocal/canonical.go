// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
