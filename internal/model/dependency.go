// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// Dependency represents a cross-branch relationship between nodes per FR-4.4.
// Dependencies are reserved for cross-branch relationships only (FR-4.1).
// Parent-child relationships are inherent in the dot-notation ID structure.
type Dependency struct {
	// FromID is the source node (e.g., the blocker).
	FromID string `json:"from_id"`

	// ToID is the target node (e.g., the blocked node).
	ToID string `json:"to_id"`

	// DepType is the kind of relationship (blocks, related, etc.).
	DepType DepType `json:"dep_type"`

	// CreatedAt is when the dependency was created (UTC).
	CreatedAt time.Time `json:"created_at"`

	// CreatedBy is the agent or user who created the dependency.
	CreatedBy string `json:"created_by"`

	// Metadata contains optional additional information.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Validate checks the dependency for correctness per FR-4.4.
// Returns ErrInvalidInput with a descriptive message for any violation.
func (d *Dependency) Validate() error {
	if d.FromID == "" {
		return fmt.Errorf("from_id is required: %w", ErrInvalidInput)
	}

	if d.ToID == "" {
		return fmt.Errorf("to_id is required: %w", ErrInvalidInput)
	}

	if d.FromID == d.ToID {
		return fmt.Errorf(
			"self-referencing dependency not allowed (%s): %w",
			d.FromID, ErrInvalidInput,
		)
	}

	if !d.DepType.IsValid() {
		return fmt.Errorf(
			"invalid dependency type %q: %w",
			d.DepType, ErrInvalidInput,
		)
	}

	return nil
}
