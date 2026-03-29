// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

// Status represents the lifecycle state of a node per FR-3.5.
type Status string

const (
	// StatusOpen is the initial state for a newly created node.
	StatusOpen Status = "open"

	// StatusInProgress indicates work has been claimed and is underway.
	StatusInProgress Status = "in_progress"

	// StatusBlocked is auto-managed: set when unresolved blockers exist (FR-3.8).
	StatusBlocked Status = "blocked"

	// StatusDone indicates the work is complete.
	StatusDone Status = "done"

	// StatusDeferred indicates work is postponed until a future date or indefinitely.
	StatusDeferred Status = "deferred"

	// StatusCancelled indicates the work has been descoped.
	StatusCancelled Status = "cancelled"

	// StatusInvalidated indicates the node's parent prompt changed, requiring re-evaluation.
	StatusInvalidated Status = "invalidated"
)

// AllStatuses returns all valid status values.
func AllStatuses() []Status {
	return []Status{
		StatusOpen,
		StatusInProgress,
		StatusBlocked,
		StatusDone,
		StatusDeferred,
		StatusCancelled,
		StatusInvalidated,
	}
}

// IsValid returns true if the status is a recognized value.
func (s Status) IsValid() bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusBlocked, StatusDone,
		StatusDeferred, StatusCancelled, StatusInvalidated:
		return true
	default:
		return false
	}
}

// IsTerminal returns true if the status represents a terminal state
// where child creation is forbidden (FR-3.9).
func (s Status) IsTerminal() bool {
	switch s {
	case StatusDone, StatusCancelled, StatusInvalidated:
		return true
	default:
		return false
	}
}
