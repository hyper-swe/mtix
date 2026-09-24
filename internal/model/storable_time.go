// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

// IsStorableTime reports whether t, converted to UTC, falls in the years
// 1..9999: the range the RFC 3339 text every stored timestamp uses can hold
// and read back (MTIX-95.22). A time outside it would be written as text such
// as "10000-01-01T04:00:00Z" or "-0001-12-31T23:30:00Z", which no reader can
// parse, so every read of the node and of its project would fail; it would
// also sort before every real time in a text comparison.
func IsStorableTime(t time.Time) bool {
	y := t.UTC().Year()
	return y >= 1 && y <= 9999
}
