// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"sort"
	"strconv"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// NaturalNodeIDLess compares two mtix dot-notation IDs using natural
// ordering per FR-17.6. The ID format is PREFIX-N.M.O... where PREFIX
// is a project identifier and each segment after the hyphen is an
// integer. Segments are compared numerically when both are valid
// integers, lexicographically otherwise (for the prefix portion).
//
// Ordering rules:
//   - PREFIX compared lexicographically (AAA < BBB)
//   - Root sequence compared numerically (PROJ-2 < PROJ-10)
//   - Each dot-separated child segment compared numerically
//   - A parent sorts before its children (PROJ-1 < PROJ-1.1)
//   - Deterministic and total: ties are broken by string length
//
// This function is O(n) in the number of segments per ID. No regex
// is used (FR-17 audit T3: no ReDoS risk). IDs are constrained by
// FR-2.1a to alphanumeric prefix + numeric dot segments.
func NaturalNodeIDLess(a, b string) bool {
	if a == b {
		return false
	}
	if a == "" {
		return true
	}
	if b == "" {
		return false
	}

	// Split into prefix and numeric path: "PROJ-1.2.3" → ("PROJ", "1.2.3")
	aPre, aPath := splitIDPrefix(a)
	bPre, bPath := splitIDPrefix(b)

	// Compare prefix lexicographically.
	if aPre != bPre {
		return aPre < bPre
	}

	// Compare numeric segments.
	aSegs := strings.Split(aPath, ".")
	bSegs := strings.Split(bPath, ".")

	minLen := len(aSegs)
	if len(bSegs) < minLen {
		minLen = len(bSegs)
	}

	for i := 0; i < minLen; i++ {
		aNum, aErr := strconv.Atoi(aSegs[i])
		bNum, bErr := strconv.Atoi(bSegs[i])

		if aErr == nil && bErr == nil {
			// Both numeric — compare as integers.
			if aNum != bNum {
				return aNum < bNum
			}
			continue
		}
		// Fallback to lexicographic for non-numeric segments.
		if aSegs[i] != bSegs[i] {
			return aSegs[i] < bSegs[i]
		}
	}

	// All compared segments equal — shorter (parent) sorts first.
	return len(aSegs) < len(bSegs)
}

// splitIDPrefix splits a mtix ID into its prefix and numeric path.
// "PROJ-1.2.3" → ("PROJ", "1.2.3")
// "PROJ-1"     → ("PROJ", "1")
// "1.2.3"      → ("", "1.2.3")
func splitIDPrefix(id string) (prefix, path string) {
	idx := strings.LastIndex(id, "-")
	if idx < 0 {
		return "", id
	}
	return id[:idx], id[idx+1:]
}

// SortNodes sorts a slice of nodes by natural dot-notation ID order
// per FR-17.6. Modifies the slice in place.
func SortNodes(nodes []*model.Node) {
	sort.Slice(nodes, func(i, j int) bool {
		return NaturalNodeIDLess(nodes[i].ID, nodes[j].ID)
	})
}
