// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// State operates on one operator directory using injected path inputs.
type State struct {
	env                   Env
	alertStage            func(string) error
	permissionReadStage   func(string)
	permissionClock       func() time.Time
	permissionNoticeProbe func(context.Context, PermissionBatch) (bool, error)
	beforeCommit          func() error
	syncDirectory         func(*os.File) error
	// probeCreate replaces os.CreateTemp in ProbeWritable; tests only.
	probeCreate func(dir, pattern string) (*os.File, error)
}

// New constructs a state accessor without creating any files.
func New(env Env) *State { return NewWithPermissionClock(env, time.Now) }

func (s *State) resolve() (string, error) {
	logical, err := Dir(s.env)
	if err != nil {
		return "", err
	}
	locationErr := validateLocation(s.env, logical)
	path := logical
	if s.env.root != "" {
		path = filepath.Join(s.env.root, strings.TrimLeft(strings.TrimPrefix(logical, filepath.VolumeName(logical)), `\/`))
	}
	return path, locationErr
}

func (s *State) directory(create bool) (*os.File, error) {
	path, err := s.resolve()
	if err != nil {
		return nil, err
	}
	f, err := openDirectory(path, create, excludedRoots(s.env))
	if err != nil {
		return nil, failure("directory "+path, err)
	}
	return f, nil
}

// Ensure creates and validates the operator directory; it never creates grants.
func (s *State) Ensure() error {
	f, err := s.directory(true)
	if err != nil {
		return err
	}
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
	if errors.Is(err, model.ErrOperatorStateUnreadable) {
		return fmt.Errorf("operator state %s: %w", reason, err)
	}
	return fmt.Errorf("operator state %s: %w: %w", reason, err, model.ErrOperatorStateUnreadable)
}

// LearnLane records an id permanently for this lane location. Repeated writes
// union identities under a protected cross-process lock; they grant nothing.
func (s *State) LearnLane(ctx context.Context, loc LaneLocation, id string) error {
	if !validLearnedID(id) {
		return invalid("learned agent id is invalid")
	}
	if _, _, err := laneRecordKeys(loc); err != nil {
		return err
	}
	return s.withRouting(ctx, true, func() error { return s.learnLaneLocked(loc, id) })
}

// LaneIDs returns all ids ever learned, falling back to the main checkout only
// after membership and the back-link have been verified on this call.
func (s *State) LaneIDs(ctx context.Context, loc LaneLocation) ([]string, error) {
	primary, fallback, err := laneRecordKeys(loc)
	if err != nil {
		return nil, err
	}
	var ids []string
	err = s.withRouting(ctx, false, func() error {
		rows, readErr := readRouting[[]learnedLane](s, "lanes")
		if readErr != nil {
			return readErr
		}
		if validationErr := validateLanes(rows); validationErr != nil {
			return validationErr
		}
		for _, key := range []laneKey{primary, fallback} {
			for _, row := range rows {
				if row.Key == key {
					ids = append([]string(nil), row.IDs...)
					sort.Strings(ids)
					return nil
				}
			}
		}
		return nil
	})
	return ids, laneReadError(err)
}

// ListBoard adds routing for a checkout without enrolling or confirming it.
func (s *State) ListBoard(ctx context.Context, key, path string) error {
	return s.ListBoardInstance(ctx, ListedBoard{Key: key, Path: path})
}

// ListedBoards reads routing records without creating a grant or state file.
func (s *State) ListedBoards(ctx context.Context) ([]ListedBoard, error) {
	boards, err := s.DispatchBoards(ctx)
	return boards.Listed, err
}
