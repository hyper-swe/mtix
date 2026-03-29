// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"regexp"
	"strings"
)

// projectPrefixRegex validates the project prefix per FR-2.1a.
// Uppercase alphanumeric and hyphens only, 1-20 characters, starting with a letter.
// Prevents SQL LIKE wildcard characters (%, _) from appearing in IDs.
var projectPrefixRegex = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,19}$`)

// ValidatePrefix checks a project prefix against the FR-2.1a regex.
// Returns ErrInvalidInput if the prefix is invalid.
func ValidatePrefix(prefix string) error {
	if !projectPrefixRegex.MatchString(prefix) {
		return fmt.Errorf(
			"invalid project prefix %q: must match ^[A-Z][A-Z0-9-]{0,19}$: %w",
			prefix, ErrInvalidInput,
		)
	}
	return nil
}

// BuildID constructs a dot-notation ID from project prefix, parent ID, and sequence.
// Root nodes: '{PREFIX}-{seq}' (e.g., 'PROJ-1')
// Child nodes: '{parent_id}.{seq}' (e.g., 'PROJ-1.3')
func BuildID(project, parentID string, seq int) string {
	if parentID == "" {
		return fmt.Sprintf("%s-%d", project, seq)
	}
	return fmt.Sprintf("%s.%d", parentID, seq)
}

// ParseIDProject extracts the project prefix from a dot-notation ID.
// E.g., 'PROJ-42.1.3' → 'PROJ'
func ParseIDProject(id string) string {
	dashIdx := strings.Index(id, "-")
	if dashIdx < 0 {
		return ""
	}
	return id[:dashIdx]
}

// ParseIDParent extracts the parent ID from a dot-notation ID.
// E.g., 'PROJ-42.1.3' → 'PROJ-42.1'
// Root nodes (e.g., 'PROJ-42') return empty string.
func ParseIDParent(id string) string {
	dotIdx := strings.LastIndex(id, ".")
	if dotIdx < 0 {
		return ""
	}
	return id[:dotIdx]
}

// ParseIDDepth computes the depth from a dot-notation ID.
// Depth = number of dots in the ID after the project-root segment.
// E.g., 'PROJ-42' → 0, 'PROJ-42.1' → 1, 'PROJ-42.1.3.2' → 3
func ParseIDDepth(id string) int {
	// Find the root segment (everything up to and including the first number after dash).
	dashIdx := strings.Index(id, "-")
	if dashIdx < 0 {
		return 0
	}

	// Count dots in the remaining portion.
	remainder := id[dashIdx:]
	return strings.Count(remainder, ".")
}

// SequenceKey builds the sequence counter key for a given project and parent.
// Format: '{project}:{parent_dotpath}' per FR-2.7.
// For root nodes (no parent): '{project}:'
func SequenceKey(project, parentID string) string {
	return project + ":" + parentID
}
