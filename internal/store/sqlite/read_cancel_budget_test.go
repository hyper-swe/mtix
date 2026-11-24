// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// A race build or -short still sweeps all 40 cancellation offsets repeatedly.
// MTIX_STRESS_READ_CANCEL=1 restores the original counts in every build.
func readCancelRouteIterations(full int) int {
	if os.Getenv("MTIX_STRESS_READ_CANCEL") == "1" {
		return full
	}
	if testing.Short() {
		return 160
	}
	return routineReadCancelIterations
}

const readCancelBudget = 4 * time.Minute

// Arm before opening the store so the watchdog also covers blocking cleanup.
// Panic from the timer: FailNow could only terminate the watchdog goroutine.
func armReadCancelDeadline(t *testing.T, budget time.Duration) {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		remaining := time.Until(deadline)
		budget = min(budget, remaining-remaining/10)
	}
	name := t.Name()
	timer := time.AfterFunc(budget, func() {
		for size := 64 << 10; ; size *= 2 {
			stack := make([]byte, size)
			if n := runtime.Stack(stack, true); n < len(stack) {
				panic("cancelled-read deadline exceeded in " + name + "\n" + string(stack[:n]))
			}
		}
	})
	t.Cleanup(func() { timer.Stop() })
}

func TestReadCancelRouteIterations_StressRequiresOptIn(t *testing.T) {
	for _, tc := range []struct {
		value  string
		stress bool
	}{
		{"", false},
		{"0", false},
		{"1", true},
	} {
		t.Run("stress="+tc.value, func(t *testing.T) {
			t.Setenv("MTIX_STRESS_READ_CANCEL", tc.value)
			iterations := readCancelRouteIterations(readCancelStressIterations)
			if tc.stress {
				require.Equal(t, 20000, iterations)
			} else {
				require.GreaterOrEqual(t, iterations, 40, "cover every cancellation offset")
				require.Less(t, iterations, 20000, "full sweep requires explicit opt-in")
			}
		})
	}
}

func TestReadCancelDeadline_BlockedTest_DumpsAllGoroutines(t *testing.T) {
	const child = "MTIX_TEST_READ_CANCEL_DEADLINE_CHILD"
	if os.Getenv(child) == "1" {
		armReadCancelDeadline(t, 25*time.Millisecond)
		time.Sleep(2 * time.Second)
		return
	}
	binary, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^"+t.Name()+"$", "-test.timeout=5s")
	cmd.Env = append(hostguard.Environ(), child+"=1", "GOTRACEBACK=none")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "over-budget child completed")
	require.Contains(t, string(out), "cancelled-read deadline exceeded in "+t.Name())
	require.Contains(t, string(out), "testing.(*M).Run", "dump must include the main goroutine")
}
