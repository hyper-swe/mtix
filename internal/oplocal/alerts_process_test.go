// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests native cross-process locking, interruption and persisted counting.
package oplocal

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAlertProcess_Helper(t *testing.T) {
	root := os.Getenv("MTIX_ALERT_TEST_ROOT")
	if root == "" {
		return
	}
	s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"APPDATA": filepath.Join(root, "config")}})
	mode := os.Getenv("MTIX_ALERT_TEST_MODE")
	if mode == "hold" {
		require.NoError(t, s.withAlerts(t.Context(), true, func(_ *os.File, _ string) error { fmt.Println("ALERT_TEST_READY"); select {} }))
		return
	}
	if mode == "crash" {
		s.alertStage = func(stage string) error {
			if stage == "after:alerts.jsonl" {
				fmt.Println("ALERT_TEST_READY")
				select {}
			}
			return nil
		}
	}
	for range 8 {
		_, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
		require.NoError(t, err)
	}
}

func alertProcessCommand(t *testing.T, s *State, mode string) *exec.Cmd {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(binary, "-test.run=^TestAlertProcess_Helper$", "-test.timeout=30s")
	cmd.Env = []string{"MTIX_ALERT_TEST_ROOT=" + s.env.Home, "MTIX_ALERT_TEST_MODE=" + mode}
	for _, key := range []string{"PATH", "HOME", "USERPROFILE", "SystemRoot", "TEMP", "TMP"} {
		cmd.Env = append(cmd.Env, key+"="+os.Getenv(key))
	}
	return cmd
}

func TestAlertRaise_ConcurrentProcesses_PreservesEveryCount(t *testing.T) {
	s := alertFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		cmd := alertProcessCommand(t, s, "count")
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := cmd.CombinedOutput()
			if err != nil {
				results <- fmt.Errorf("alert helper: %w (%s)", err, data)
			} else {
				results <- nil
			}
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	rows, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(32), rows[0].Count)
}

func startHeldAlertProcess(t *testing.T, s *State, mode string) *exec.Cmd {
	t.Helper()
	cmd := alertProcessCommand(t, s, mode)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			require.NoError(t, cmd.Process.Kill())
			require.Error(t, cmd.Wait())
		}
	})
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			if scanner.Text() == "ALERT_TEST_READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		require.True(t, ok)
	case <-time.After(10 * time.Second):
		t.Fatal("alert helper did not reach native checkpoint")
	}
	// Ownership of the process transfers to the test after this checkpoint.
	return cmd
}

func stopHeldAlertProcess(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
}

func TestAlertRaise_ProcessDeath_ReleasesLockAndRecoversAcceptedRow(t *testing.T) {
	for _, mode := range []string{"hold", "crash"} {
		t.Run(mode, func(t *testing.T) {
			s := alertFixture(t)
			cmd := startHeldAlertProcess(t, s, mode)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			_, err := s.RecordAlert(ctx, alertInput(), alertTime())
			cancel()
			require.ErrorIs(t, err, context.DeadlineExceeded)
			stopHeldAlertProcess(t, cmd)
			rows, err := s.RecentAlerts(t.Context(), 0)
			require.NoError(t, err)
			if mode == "crash" {
				require.Len(t, rows, 1)
				require.Equal(t, uint64(1), rows[0].Count)
			} else {
				require.Empty(t, rows)
			}
			row, err := New(s.env).RecordAlert(t.Context(), alertInput(), alertTime())
			require.NoError(t, err)
			expected := uint64(1)
			if mode == "crash" {
				expected = 2
			}
			require.Equal(t, expected, row.Count)
		})
	}
}
