//go:build !race

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import "time"

// historyWriteBudget is the median write latency allowed on the 100k board:
// the NFR-1.1 target for a create.
const historyWriteBudget = 10 * time.Millisecond
