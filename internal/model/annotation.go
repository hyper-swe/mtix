// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

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
}
