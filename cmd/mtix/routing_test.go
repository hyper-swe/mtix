// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsExemptCommand_ConfigIsExempt verifies config command bypasses routing.
func TestIsExemptCommand_ConfigIsExempt(t *testing.T) {
	cmd := &cobra.Command{Use: "config"}
	assert.True(t, isExemptCommand(cmd), "config should be exempt")
}

// TestIsExemptCommand_InitIsExempt verifies init command bypasses routing.
func TestIsExemptCommand_InitIsExempt(t *testing.T) {
	cmd := &cobra.Command{Use: "init"}
	assert.True(t, isExemptCommand(cmd), "init should be exempt")
}

// TestIsExemptCommand_MigrateIsExempt verifies migrate command bypasses routing.
func TestIsExemptCommand_MigrateIsExempt(t *testing.T) {
	cmd := &cobra.Command{Use: "migrate"}
	assert.True(t, isExemptCommand(cmd), "migrate should be exempt")
}

// TestIsExemptCommand_DocsIsExempt verifies docs command bypasses routing.
func TestIsExemptCommand_DocsIsExempt(t *testing.T) {
	cmd := &cobra.Command{Use: "docs"}
	assert.True(t, isExemptCommand(cmd), "docs should be exempt")
}

// TestIsExemptCommand_VersionIsExempt verifies version command bypasses routing.
func TestIsExemptCommand_VersionIsExempt(t *testing.T) {
	cmd := &cobra.Command{Use: "version"}
	assert.True(t, isExemptCommand(cmd), "version should be exempt")
}

// TestIsExemptCommand_ConfigSubcommandIsExempt verifies config subcommands bypass routing.
func TestIsExemptCommand_ConfigSubcommandIsExempt(t *testing.T) {
	parent := &cobra.Command{Use: "config"}
	child := &cobra.Command{Use: "get"}
	parent.AddCommand(child)
	assert.True(t, isExemptCommand(child), "config get should be exempt")
}

// TestIsExemptCommand_ShowIsNotExempt verifies show command is routed.
func TestIsExemptCommand_ShowIsNotExempt(t *testing.T) {
	cmd := &cobra.Command{Use: "show"}
	assert.False(t, isExemptCommand(cmd), "show should not be exempt")
}

// TestIsExemptCommand_ListIsNotExempt verifies list command is routed.
func TestIsExemptCommand_ListIsNotExempt(t *testing.T) {
	cmd := &cobra.Command{Use: "list"}
	assert.False(t, isExemptCommand(cmd), "list should not be exempt")
}

// TestReadPIDLock_MissingFile_ReturnsNotAlive verifies missing lock handled.
func TestReadPIDLock_MissingFile_ReturnsNotAlive(t *testing.T) {
	tmpDir := t.TempDir()
	port, alive := readPIDLock(tmpDir)
	assert.Equal(t, 0, port)
	assert.False(t, alive)
}

// TestReadPIDLock_InvalidContent_ReturnsNotAlive verifies invalid content handled.
func TestReadPIDLock_InvalidContent_ReturnsNotAlive(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, pidLockFile)

	require.NoError(t, os.WriteFile(lockPath, []byte("not-a-number\n"), 0o600))

	port, alive := readPIDLock(tmpDir)
	assert.Equal(t, 0, port)
	assert.False(t, alive)
}

// TestReadPIDLock_StalePID_ReturnsNotAlive verifies dead process detected.
func TestReadPIDLock_StalePID_ReturnsNotAlive(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, pidLockFile)

	// Use a very high PID that's almost certainly not running.
	require.NoError(t, os.WriteFile(lockPath, []byte("999999999\n6849\n"), 0o600))

	port, alive := readPIDLock(tmpDir)
	assert.Equal(t, 0, port)
	assert.False(t, alive, "dead process should be detected")

	// Stale lock should be cleaned up.
	_, err := os.Stat(lockPath)
	assert.True(t, os.IsNotExist(err), "stale lock file should be removed")
}

// TestReadPIDLock_CurrentPID_ReturnsAlive verifies live process detected.
func TestReadPIDLock_CurrentPID_ReturnsAlive(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, pidLockFile)

	// Use the current process PID — guaranteed to be alive.
	content := fmt.Sprintf("%d\n6849\n", os.Getpid())
	require.NoError(t, os.WriteFile(lockPath, []byte(content), 0o600))

	port, alive := readPIDLock(tmpDir)
	assert.Equal(t, 6849, port)
	assert.True(t, alive, "current process should be alive")
}

// TestWritePIDLock_CreatesFile verifies lock file is created correctly.
func TestWritePIDLock_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()

	err := writePIDLock(tmpDir, 6849)
	require.NoError(t, err)

	lockPath := filepath.Join(tmpDir, pidLockFile)
	data, err := os.ReadFile(lockPath)
	require.NoError(t, err)

	expected := fmt.Sprintf("%d\n6849\n", os.Getpid())
	assert.Equal(t, expected, string(data))
}

// TestRemovePIDLock_RemovesFile verifies lock file is removed.
func TestRemovePIDLock_RemovesFile(t *testing.T) {
	tmpDir := t.TempDir()

	require.NoError(t, writePIDLock(tmpDir, 6849))
	removePIDLock(tmpDir)

	lockPath := filepath.Join(tmpDir, pidLockFile)
	_, err := os.Stat(lockPath)
	assert.True(t, os.IsNotExist(err), "lock file should be removed")
}

// TestRemovePIDLock_NoFile_NoPanic verifies removing nonexistent lock is safe.
func TestRemovePIDLock_NoFile_NoPanic(t *testing.T) {
	tmpDir := t.TempDir()
	// Should not panic.
	removePIDLock(tmpDir)
}

// TestIsProcessAlive_CurrentProcess_ReturnsTrue verifies current process check.
func TestIsProcessAlive_CurrentProcess_ReturnsTrue(t *testing.T) {
	assert.True(t, isProcessAlive(os.Getpid()))
}

// TestIsProcessAlive_DeadProcess_ReturnsFalse verifies dead process check.
func TestIsProcessAlive_DeadProcess_ReturnsFalse(t *testing.T) {
	// PID 999999999 is almost certainly not running.
	assert.False(t, isProcessAlive(999999999))
}

// TestShouldRouteToServer_ExemptCommand_ReturnsZero verifies exempt commands skip routing.
func TestShouldRouteToServer_ExemptCommand_ReturnsZero(t *testing.T) {
	cmd := &cobra.Command{Use: "config"}
	port := shouldRouteToServer(cmd)
	assert.Equal(t, 0, port, "exempt command should return 0")
}

// TestAdminRoutes_AllMapped verifies all admin commands have routes.
func TestAdminRoutes_AllMapped(t *testing.T) {
	expected := []string{"backup", "export", "import", "gc", "verify"}
	for _, cmd := range expected {
		route, ok := adminRoutes[cmd]
		assert.True(t, ok, "admin route for %s should exist", cmd)
		assert.NotEmpty(t, route.method, "method for %s should be set", cmd)
		assert.NotEmpty(t, route.path, "path for %s should be set", cmd)
	}
}

// TestAdminRoutes_CorrectEndpoints verifies admin endpoint mappings per FR-14.1b.
func TestAdminRoutes_CorrectEndpoints(t *testing.T) {
	tests := []struct {
		cmd    string
		method string
		path   string
	}{
		{"backup", "POST", "/admin/backup"},
		{"export", "GET", "/admin/export"},
		{"import", "POST", "/admin/import"},
		{"gc", "POST", "/admin/gc"},
		{"verify", "GET", "/admin/verify"},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			route := adminRoutes[tt.cmd]
			assert.Equal(t, tt.method, route.method)
			assert.Equal(t, tt.path, route.path)
		})
	}
}
