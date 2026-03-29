// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"regexp"
	"strings"
	"testing"
)

// FuzzValidatePrefix tests project prefix validation does not panic and
// if validation passes, the prefix conforms to ^[A-Z][A-Z0-9-]{0,19}$.
func FuzzValidatePrefix(f *testing.F) {
	// Seed corpus: valid and invalid prefixes.
	f.Add("PROJ")
	f.Add("A")
	f.Add("")
	f.Add("proj")
	f.Add("ABCDEFGHIJKLMNOPQRSTU")
	f.Add("A-B-C")
	f.Add("123")
	f.Add("-ABC")
	f.Add("A B")
	f.Add("A\x00B")
	f.Add(strings.Repeat("A", 100))

	validRegex := regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,19}$`)

	f.Fuzz(func(t *testing.T, prefix string) {
		err := ValidatePrefix(prefix)
		if err == nil {
			// If validation passes, the prefix MUST match the regex.
			if !validRegex.MatchString(prefix) {
				t.Errorf("ValidatePrefix(%q) returned nil but doesn't match regex", prefix)
			}
		}
		// No panics is itself a success.
	})
}

// FuzzParseDotNotationID tests ID parsing functions don't panic and
// produce consistent results.
func FuzzParseDotNotationID(f *testing.F) {
	// Seed corpus: valid and invalid IDs.
	f.Add("PROJ-42")
	f.Add("PROJ-42.1.3.2")
	f.Add("A-1")
	f.Add("TEST-100.50.25")
	f.Add("")
	f.Add("-")
	f.Add(".")
	f.Add("PROJ-")
	f.Add("PROJ-0")
	f.Add("A-1.1.1.1.1.1.1.1.1.1")
	f.Add("PROJ-42.1.3")

	f.Fuzz(func(t *testing.T, id string) {
		// These functions should never panic regardless of input.
		project := ParseIDProject(id)
		parent := ParseIDParent(id)
		depth := ParseIDDepth(id)

		// Property: depth should be non-negative.
		if depth < 0 {
			t.Errorf("ParseIDDepth(%q) returned %d, expected >= 0", id, depth)
		}

		// Property: if ID has no dash, project should be empty.
		if !strings.Contains(id, "-") {
			if project != "" {
				t.Errorf("ParseIDProject(%q) returned %q, expected empty for ID without dash", id, project)
			}
		}

		// Property: if ID has no dot, parent should be empty.
		if !strings.Contains(id, ".") {
			if parent != "" {
				t.Errorf("ParseIDParent(%q) returned %q, expected empty for ID without dot", id, parent)
			}
		}

		// Property: if parent is non-empty, it should be a prefix of the ID.
		if parent != "" && !strings.HasPrefix(id, parent) {
			t.Errorf("ParseIDParent(%q) = %q is not a prefix of the ID", id, parent)
		}
	})
}

// FuzzBuildID tests ID construction doesn't panic.
func FuzzBuildID(f *testing.F) {
	// Seed corpus.
	f.Add("PROJ", "", 1)
	f.Add("PROJ", "PROJ-1", 3)
	f.Add("A", "A-1.2", 5)
	f.Add("", "", 0)
	f.Add("TEST", "TEST-100", 999)

	f.Fuzz(func(t *testing.T, project, parentID string, seq int) {
		// Should never panic.
		result := BuildID(project, parentID, seq)

		// Property: result should be non-empty if project is non-empty.
		if project != "" && result == "" {
			t.Errorf("BuildID(%q, %q, %d) returned empty string", project, parentID, seq)
		}

		// Property: root IDs (no parent) should contain the project prefix.
		if parentID == "" && project != "" {
			if !strings.HasPrefix(result, project+"-") {
				t.Errorf("BuildID(%q, \"\", %d) = %q doesn't start with %s-",
					project, seq, result, project)
			}
		}

		// Property: child IDs should start with parent.
		if parentID != "" {
			if !strings.HasPrefix(result, parentID+".") {
				t.Errorf("BuildID(%q, %q, %d) = %q doesn't start with %s.",
					project, parentID, seq, result, parentID)
			}
		}
	})
}
