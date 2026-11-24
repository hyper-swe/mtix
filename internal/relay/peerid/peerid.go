// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package peerid defines the relay identity grammar shared by files and cursors.
package peerid

import "regexp"

var pattern = regexp.MustCompile(`^[0-9a-f]{16}(-[a-z0-9_-]{1,32})?$`)

// Valid reports whether id is a 16-digit lowercase machine hash with an
// optional operator label as specified by FR-21 §5.2.
func Valid(id string) bool {
	return pattern.MatchString(id)
}
