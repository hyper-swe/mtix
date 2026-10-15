// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Test watchdogs follow the test deadline independently of the production budget.
package service

import (
	"context"
	"testing"
	"time"
)

func boardTestWatchdog(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		return context.WithDeadline(context.Background(), deadline.Add(-3*time.Second))
	}
	return context.WithCancel(context.Background())
}
