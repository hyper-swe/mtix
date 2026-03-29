// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import "time"

// testClock returns a fixed-time clock for deterministic tests.
func testClock() func() time.Time {
	fixed := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return fixed }
}
