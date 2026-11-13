// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReadLoopPID_PartialOrInvalidContent_IsNotReady pins MTIX-260: the pid
// file is only ready once it holds a parseable positive pid. An empty file is
// exactly what the shell leaves between truncating and writing it.
func TestReadLoopPID_PartialOrInvalidContent_IsNotReady(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
		ready   bool
	}{
		{"empty file (truncated, not yet written)", "", 0, false},
		{"whitespace only", "\n", 0, false},
		{"not a number", "abc\n", 0, false},
		{"zero is not a pid", "0\n", 0, false},
		{"negative is not a pid", "-5\n", 0, false},
		{"complete pid", "4242\n", 4242, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWaiterEnv(t)
			require.NoError(t, os.WriteFile(e.loopPIDFile("w"), []byte(tt.content), 0o600))
			pid, ok := e.readLoopPID("w")
			require.Equal(t, tt.ready, ok)
			require.Equal(t, tt.want, pid)
		})
	}
}

func TestReadLoopPID_MissingFile_IsNotReady(t *testing.T) {
	e := newWaiterEnv(t)
	pid, ok := e.readLoopPID("absent")
	require.False(t, ok)
	require.Zero(t, pid)
}

// TestLoopScript_PIDFile_IsNeverObservedEmpty runs the real wrapped command
// many times while a reader polls the way the old readiness wait did (Stat,
// then read) and requires that every file it sees already holds a pid. With a
// plain "echo $$ >file" the shell truncates the file first, so an empty file
// is visible between open and write (MTIX-260).
func TestLoopScript_PIDFile_IsNeverObservedEmpty(t *testing.T) {
	e := newWaiterEnv(t)
	require.NoError(t, os.WriteFile(e.release, nil, 0o600)) // the command exits as soon as it has recorded its pid
	var torn atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := os.Stat(e.loopPIDFile("w")); err != nil {
				continue
			}
			data, err := os.ReadFile(e.loopPIDFile("w")) //nolint:gosec // G304: path under this test's TempDir
			if err == nil && len(data) == 0 {
				torn.Add(1)
			}
		}
	}()
	const runs = 300
	for i := 0; i < runs; i++ {
		_ = os.Remove(e.loopPIDFile("w"))
		out, err := exec.Command("sh", "-c", loopScript, "sh", e.release, "0", e.loopPIDFile("w")).CombinedOutput() //nolint:gosec // G204: fixed script, test-owned paths
		require.NoError(t, err, string(out))
	}
	close(stop)
	<-done
	require.Zero(t, torn.Load(), "the pid file was visible but empty "+strconv.Itoa(int(torn.Load()))+" times in "+strconv.Itoa(runs)+" runs")
}

// TestWaitLoopPID_FileEmptyThenPartialThenComplete_WaitsForTheParsedPID pins
// the parsed-pid wait on its own, independent of the atomic rename in
// loopScript: the test writes the pid file itself, leaving it empty and then
// unparseable across several polls before the pid lands. A wait that returns
// on existence, or after one read, returns early or with pid 0 (MTIX-260).
func TestWaitLoopPID_FileEmptyThenPartialThenComplete_WaitsForTheParsedPID(t *testing.T) {
	e := newWaiterEnv(t)
	path := e.loopPIDFile("w")
	polls := 0
	got := e.waitLoopPIDObserved(t, "w", func(poll int) {
		polls = poll
		var content string
		switch {
		case poll == 1:
			content = "" // created and truncated, nothing written yet
		case poll < 5:
			content = "ab" // present but not a pid
		case poll < 7:
			content = "-1\n"
		default:
			content = "4242\n"
		}
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	})
	require.Equal(t, 4242, got)
	require.GreaterOrEqual(t, polls, 7, "the wait returned before the pid was written")
}
