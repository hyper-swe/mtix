// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"path/filepath"
	"slices"
	"strings"
)

// finishResolved canonicalizes the part that exists and attaches the rest.
func finishResolved(existing string, rest []string) string {
	if resolved, err := filepath.EvalSymlinks(existing); err == nil {
		existing = resolved
	}
	return filepath.Join(append([]string{existing}, rest...)...)
}

// splitPathComponents splits on every separator the platform accepts.
func splitPathComponents(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == filepath.Separator })
}

// hasParentSegment reports a ".." component in a program path as written.
func hasParentSegment(p string) bool {
	return slices.Contains(splitPathComponents(p), "..")
}
