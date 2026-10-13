//go:build race

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import "time"

// historyWriteBudget is the median write latency allowed on the 100k board
// under the race detector, which slows SQLite about 20x; the planner test
// (TestHistorySnapshotSQL_UsesPrimaryKeyIndex) is the deterministic guard.
const historyWriteBudget = 20 * time.Millisecond
