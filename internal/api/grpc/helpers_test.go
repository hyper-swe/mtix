// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"time"
)

// testClock returns a deterministic clock for testing per FR-8.1.
func testClock() func() time.Time {
	fixed := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return fixed }
}
