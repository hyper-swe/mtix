// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// State operates on one operator directory using injected path inputs.
type State struct {
	env          Env
	path         string
	beforeCommit func() error
}

// New constructs a state accessor without creating any files.
func New(env Env) *State { return &State{env: env} }

func (s *State) resolve() (string, error) {
	logical, err := Dir(s.env)
	if err != nil {
		return "", err
	}
	locationErr := validateLocation(s.env, logical)
	path := logical
	if s.env.Root != "" {
		path = filepath.Join(s.env.Root, strings.TrimLeft(strings.TrimPrefix(logical, filepath.VolumeName(logical)), `\/`))
	}
	return path, locationErr
}

func (s *State) directory(create bool) (*os.File, error) {
	path, err := s.resolve()
	if err != nil {
		return nil, err
	}
	f, err := openDirectory(path, create)
	if err != nil {
		return nil, fmt.Errorf("operator directory %s: %w: %w", path, err, invalid("validate input"))
	}
	return f, nil
}

// Ensure creates and validates the operator directory; it never creates grants.
func (s *State) Ensure() error {
	f, err := s.directory(true)
	if err != nil {
		return err
	}
	s.path = f.Name()
	return closeFile(f)
}

func closeFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("close operator state: %w: %w", err, invalid("close failed"))
	}
	return nil
}

func closeWith(f *os.File, err *error) { *err = errors.Join(*err, closeFile(f)) }

// Path resolves the validated configured path without accessing its contents.
func (s *State) Path() (string, error) { return s.resolve() }

func failure(reason string, err error) error {
	return fmt.Errorf("operator state %s: %w: %w", reason, err, invalid(reason))
}
