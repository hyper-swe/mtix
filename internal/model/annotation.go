// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"cmp"
	"slices"
	"time"
)

// Annotation represents a human annotation on a node's prompt per FR-3.4.
type Annotation struct {
	// ID is a ULID for sortability.
	ID string `json:"id"`

	// Author is the agent ID or human email that created the annotation.
	Author string `json:"author"`

	// Text is the annotation content.
	Text string `json:"text"`

	// CreatedAt is when the annotation was created (UTC).
	CreatedAt time.Time `json:"created_at"`

	// Resolved indicates whether the annotation has been addressed.
	Resolved bool `json:"resolved"`

	// Addressee is the agent id this comment is directed at (FR-19.1), set via
	// `mtix comment --to <agent>` or an @<agent> token. Empty for an ordinary
	// comment. Carried onto the emitted comment event's payload so the
	// addressee's inbox (a query over the journal) surfaces it.
	Addressee string `json:"addressee,omitempty"`
	// Hop is transient send metadata, retained only in the comment event payload.
	Hop int64 `json:"-"`
}

// SortAnnotations returns a copy of annotations ordered by (CreatedAt, ID),
// oldest first, with equal timestamps ordered by id. Every path that stores
// or presents annotations uses it, so replicas and outputs agree on the order.
func SortAnnotations(annotations []Annotation) []Annotation {
	sorted := slices.Clone(annotations)
	slices.SortStableFunc(sorted, func(a, b Annotation) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return sorted
}
