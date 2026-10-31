// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// LaneLocation describes a caller's checkout and working directory. Membership
// is rediscovered from files on every use; callers cannot supply a common dir.
type LaneLocation struct{ Harness, BoardKey, Root, Path string }

type laneKey struct {
	Harness    string `json:"harness"`
	Repository string `json:"repository"`
	Relative   string `json:"relative"`
}
type learnedLane struct {
	Key laneKey  `json:"location"`
	IDs []string `json:"ids"`
}

// ListedBoard is routing data, never an enrollment or confirmation grant.
type ListedBoard struct {
	Key             string `json:"board_key"`
	Path            string `json:"path"`
	StoreInstanceID string `json:"store_instance_id,omitempty"`
}

var laneHarness = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

func validLearnedID(id string) bool {
	return len(id) <= 64 && strings.TrimSpace(id) != "" && alertTextValid(id)
}

func validRoutingBoard(key string) bool {
	id, err := uuid.Parse(key)
	return err == nil && id != uuid.Nil && id.String() == key
}

func laneRecordKeys(loc LaneLocation) (laneKey, laneKey, error) {
	root, relative, common, _, err := lanePaths(loc)
	if err != nil {
		return laneKey{}, laneKey{}, err
	}
	repository := "path:" + root
	if common != "" {
		repository = "git:" + common
	}
	fallback := laneKey{loc.Harness, repository, relative}
	if loc.BoardKey != "" {
		repository = "board:" + loc.BoardKey
	}
	primary := laneKey{loc.Harness, repository, relative}
	if common == "" {
		fallback = primary
	}
	return primary, fallback, nil
}

func readRouting[T any](s *State, name string) (T, error) {
	var value T
	reports, err := s.ReadJSON(name, &value)
	if err != nil {
		return value, err
	}
	if len(reports) > 0 {
		return value, invalid("routing record belongs to another host")
	}
	return value, nil
}

func validateLanes(rows []learnedLane) error {
	seen := map[laneKey]bool{}
	for _, row := range rows {
		if !laneHarness.MatchString(row.Key.Harness) || row.Key.Repository == "" || seen[row.Key] || len(row.IDs) == 0 {
			return invalid("learned lane record is invalid")
		}
		seen[row.Key] = true
		ids := map[string]bool{}
		for _, id := range row.IDs {
			if !validLearnedID(id) || ids[id] {
				return invalid("learned lane id is invalid")
			}
			ids[id] = true
		}
	}
	return nil
}

func mergeLearned(rows []learnedLane, key laneKey, id string) []learnedLane {
	for i := range rows {
		if rows[i].Key == key {
			for _, known := range rows[i].IDs {
				if known == id {
					return rows
				}
			}
			rows[i].IDs = append(rows[i].IDs, id)
			sort.Strings(rows[i].IDs)
			return rows
		}
	}
	return append(rows, learnedLane{Key: key, IDs: []string{id}})
}

func (s *State) learnLaneLocked(loc LaneLocation, id string) error {
	primary, fallback, err := laneRecordKeys(loc)
	if err != nil {
		return err
	}
	rows, err := readRouting[[]learnedLane](s, "lanes")
	if err != nil {
		return err
	}
	if validationErr := validateLanes(rows); validationErr != nil {
		return validationErr
	}
	rows = mergeLearned(rows, primary, id)
	_, _, _, main, err := lanePaths(loc)
	if err != nil {
		return err
	}
	if main && fallback != primary {
		rows = mergeLearned(rows, fallback, id)
	}
	return s.WriteJSON("lanes", rows)
}

func (s *State) listedBoardsLocked() ([]ListedBoard, error) {
	rows, err := readRouting[[]ListedBoard](s, "listed-boards")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if !validRoutingBoard(row.Key) || !cleanAbsolute(row.Path) || seen[row.Path] {
			return nil, invalid("listed board routing record is invalid")
		}
		seen[row.Path] = true
	}
	return rows, nil
}

func (s *State) withRouting(ctx context.Context, create bool, fn func() error) error {
	// Reuse the protected cross-process transaction lock, without entering alert
	// recording while it is held. Routing writes never modify grant records.
	return s.withAlerts(ctx, create, func(_ *os.File, _ string) error { return fn() })
}

func laneReadError(err error) error {
	if err != nil {
		return fmt.Errorf("read learned lane: %w", err)
	}
	return nil
}
