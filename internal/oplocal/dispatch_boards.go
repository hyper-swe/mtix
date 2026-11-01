// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// EnrolledBoard is explicit checkout-scoped authority, never inferred from a listing.
type EnrolledBoard struct {
	Path            string      `json:"path"`
	BoardID         string      `json:"board_id"`
	StoreInstanceID string      `json:"store_instance_id"`
	Hub             HubIdentity `json:"hub_identity"`
}

// EnrollmentAudit retains the operator source and time, separate from neutral board activity.
type EnrollmentAudit struct {
	Path   string    `json:"path"`
	Action string    `json:"action"`
	Mode   string    `json:"mode"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

// DispatchBoards is the protected host registry used by FR-20.4.
type DispatchBoards struct {
	Listed   []ListedBoard     `json:"listed"`
	Enrolled []EnrolledBoard   `json:"enrolled"`
	History  []EnrollmentAudit `json:"history,omitempty"`
}

// DispatchBoards reads host-bound state without creating it or granting authority.
func (s *State) DispatchBoards(ctx context.Context) (DispatchBoards, error) {
	var out DispatchBoards
	err := s.withRouting(ctx, false, func() error { var err error; out, err = s.dispatchBoardsLocked(); return err })
	return out, err
}

func (s *State) dispatchBoardsLocked() (DispatchBoards, error) {
	out, err := readRouting[DispatchBoards](s, "boards")
	if err != nil {
		return out, err
	}
	old, err := s.listedBoardsLocked()
	if err != nil {
		return DispatchBoards{}, err
	}
	for _, row := range old {
		found := false
		for _, current := range out.Listed {
			if current.Path == row.Path {
				if current.Key != row.Key && row.Key != current.StoreInstanceID {
					return DispatchBoards{}, invalid("conflicting listed board identity")
				}
				found = true
			}
		}
		if !found {
			out.Listed = append(out.Listed, row)
		}
	}
	if err := validateDispatchBoards(out); err != nil {
		return DispatchBoards{}, err
	}
	sort.Slice(out.Listed, func(i, j int) bool { return out.Listed[i].Path < out.Listed[j].Path })
	return out, nil
}

func validateDispatchBoards(b DispatchBoards) error {
	seen, err := indexListedBoards(b.Listed)
	if err != nil {
		return err
	}
	if err := validateEnrolledBoards(b.Enrolled, seen); err != nil {
		return err
	}
	for _, row := range b.History {
		if !cleanAbsolute(row.Path) || (row.Action != "enroll" && row.Action != "unenroll") || !validEnrollmentMode(row.Mode) || row.At.IsZero() || row.Source == "" || !alertTextValid(row.Source) {
			return invalid("enrollment audit is invalid")
		}
	}
	return nil
}

func indexListedBoards(rows []ListedBoard) (map[string]ListedBoard, error) {
	seen := map[string]ListedBoard{}
	for _, row := range rows {
		if !validRoutingBoard(row.Key) || !cleanAbsolute(row.Path) || (row.StoreInstanceID != "" && !validRoutingBoard(row.StoreInstanceID)) {
			return nil, invalid("listed board identity is invalid")
		}
		if _, ok := seen[row.Path]; ok {
			return nil, invalid("duplicate listed checkout")
		}
		seen[row.Path] = row
	}
	return seen, nil
}

func validateEnrolledBoards(rows []EnrolledBoard, seen map[string]ListedBoard) error {
	enrolled := map[string]bool{}
	for _, row := range rows {
		listed, ok := seen[row.Path]
		if !ok || enrolled[row.Path] || row.BoardID != listed.Key || row.StoreInstanceID == "" || row.StoreInstanceID != listed.StoreInstanceID || validateHub(row.Hub) != nil {
			return invalid("enrolled checkout is invalid")
		}
		enrolled[row.Path] = true
	}
	return nil
}

func validEnrollmentMode(mode string) bool {
	return mode == "interactive" || mode == "yes" || mode == "file"
}

// ListBoardInstance binds routing to a locally observed store creation identity.
// The legacy ListBoard call never erases an existing binding.
func (s *State) ListBoardInstance(ctx context.Context, row ListedBoard) error {
	if !validRoutingBoard(row.Key) || !cleanAbsolute(row.Path) || (row.StoreInstanceID != "" && !validRoutingBoard(row.StoreInstanceID)) {
		return invalid("listed board identity or path is invalid")
	}
	return s.withRouting(ctx, true, func() error {
		b, err := s.dispatchBoardsLocked()
		if err != nil {
			return err
		}
		for i, old := range b.Listed {
			if old.Path == row.Path {
				if err := updateListedBoard(&b, i, row); err != nil {
					return err
				}
				return s.WriteJSON("boards", b)
			}
		}
		b.Listed = append(b.Listed, row)
		sort.Slice(b.Listed, func(i, j int) bool { return b.Listed[i].Path < b.Listed[j].Path })
		return s.WriteJSON("boards", b)
	})
}

// updateListedBoard permits the initial fallback identity to acquire its committed board id.
func updateListedBoard(b *DispatchBoards, i int, row ListedBoard) error {
	old := b.Listed[i]
	if old.StoreInstanceID != "" && row.StoreInstanceID != "" && old.StoreInstanceID != row.StoreInstanceID {
		return fmt.Errorf("listed board identity changed: %w", model.ErrConflict)
	}
	rekey := old.Key == old.StoreInstanceID && old.StoreInstanceID != "" && old.StoreInstanceID == row.StoreInstanceID
	if old.Key != row.Key && !rekey {
		return fmt.Errorf("listed board identity changed: %w", model.ErrConflict)
	}
	if row.StoreInstanceID == "" {
		return nil
	}
	b.Listed[i] = row
	rekeyEnrolledBoard(b, old, row)
	return nil
}

func rekeyEnrolledBoard(b *DispatchBoards, old, row ListedBoard) {
	for j, entry := range b.Enrolled {
		if entry.Path == old.Path && entry.StoreInstanceID == row.StoreInstanceID && entry.BoardID == old.Key {
			b.Enrolled[j].BoardID = row.Key
		}
	}
}

// SetEnrollment changes one checkout grant with its audit in the same durable record.
// Callers supply independently verified identities; nil entry means unenroll.
func (s *State) SetEnrollment(ctx context.Context, entry *EnrolledBoard, audit EnrollmentAudit) error {
	return s.withRouting(ctx, true, func() error {
		b, err := s.dispatchBoardsLocked()
		if err != nil {
			return err
		}
		var next []EnrolledBoard
		for _, row := range b.Enrolled {
			if row.Path != audit.Path {
				next = append(next, row)
			}
		}
		if entry != nil {
			if entry.Path != audit.Path || audit.Action != "enroll" {
				return invalid("enrollment audit does not match checkout")
			}
			next = append(next, *entry)
		} else if audit.Action != "unenroll" {
			return invalid("unenrollment audit is invalid")
		}
		b.Enrolled = next
		b.History = append(b.History, audit)
		if err := validateDispatchBoards(b); err != nil {
			return err
		}
		return s.WriteJSON("boards", b)
	})
}
