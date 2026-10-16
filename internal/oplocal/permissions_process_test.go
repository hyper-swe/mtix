// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
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

func TestPermissions_ProcessHelper(t *testing.T) {
	root := os.Getenv("MTIX_PERMISSION_TEST_ROOT")
	if root == "" {
		return
	}
	s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"APPDATA": filepath.Join(root, "config")}})
	if os.Getenv("MTIX_PERMISSION_OCCURRENCE") == "1" {
		_, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), permissionAlertOccurrence())
		require.NoError(t, err)
		return
	}
	batch := permissionFixtureBatch()
	batch.Changes[0].AgentID = os.Getenv("MTIX_PERMISSION_TEST_AGENT")
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
}
func TestPermissions_CrossProcessWriters(t *testing.T) {
	s := alertFixture(t)
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for i := range 4 {
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestPermissions_ProcessHelper$", "-test.timeout=10s")
		cmd.Env = []string{"MTIX_PERMISSION_TEST_ROOT=" + s.env.Home, fmt.Sprintf("MTIX_PERMISSION_TEST_AGENT=process-%d", i)}
		for _, key := range []string{"PATH", "HOME", "USERPROFILE", "SystemRoot", "TEMP", "TMP"} {
			cmd.Env = append(cmd.Env, key+"="+os.Getenv(key))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			output, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("permission helper: %w (%s)", err, output)
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 4)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 4)
}

func TestPermissions_PublisherReentrantReadsDeny(t *testing.T) {
	if os.Getenv("MTIX_PERMISSION_REENTRANT_CHILD") == "1" {
		permissionReentrantReadsDeny(t)
		return
	}
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestPermissions_PublisherReentrantReadsDeny$", "-test.timeout=6s")
	cmd.Env = []string{"MTIX_PERMISSION_REENTRANT_CHILD=1"}
	for _, key := range []string{"PATH", "HOME", "USERPROFILE", "SystemRoot", "TEMP", "TMP"} {
		cmd.Env = append(cmd.Env, key+"="+os.Getenv(key))
	}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestPermissionAlert_CrossProcessSameKeyCountsOnce(t *testing.T) {
	s := alertFixture(t)
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestPermissions_ProcessHelper$", "-test.timeout=10s")
		cmd.Env = []string{"MTIX_PERMISSION_TEST_ROOT=" + s.env.Home, "MTIX_PERMISSION_OCCURRENCE=1"}
		for _, key := range []string{"PATH", "HOME", "USERPROFILE", "SystemRoot", "TEMP", "TMP"} {
			cmd.Env = append(cmd.Env, key+"="+os.Getenv(key))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			output, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("occurrence helper: %w (%s)", err, output)
			}
			results <- err
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
	require.Equal(t, uint64(1), rows[0].Count)
}
