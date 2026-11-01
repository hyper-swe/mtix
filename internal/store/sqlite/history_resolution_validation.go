// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import "strings"

// validResolutionMarker enforces the value-free receipt contract on every
// import carrier (MTIX-95.31.19.7). The selector names a side and a set of
// current workflow fields; it can never carry values or arbitrary text.
func validResolutionMarker(field string, old, next *string) bool {
	if old != nil || next != nil {
		return false
	}
	parts := strings.Split(field, ":")
	if len(parts) != 3 || parts[0] != "resolution" || (parts[1] != "ours" && parts[1] != "theirs") {
		return false
	}
	fields := strings.Split(parts[2], ",")
	if len(fields) > 5 {
		return false
	}
	seen := make(map[string]bool, len(fields))
	for _, name := range fields {
		switch name {
		case "status", "assignee", "agent_state", "defer_until", "deleted_at":
		default:
			return false
		}
		if seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}
