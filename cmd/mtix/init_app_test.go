// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// initApp and run tests
// ============================================================================

// TestInitApp_NoMtixDir_ReturnsNilAndSetsLogger verifies graceful handling of missing .mtix.
func TestInitApp_NoMtixDir_ReturnsNilAndSetsLogger(t *testing.T) {
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	old := app
	defer func() { app = old }()
	app = appContext{}

	cmd := &cobra.Command{Use: "test"}
	err = initApp(cmd, "info")
	assert.NoError(t, err)
	assert.NotNil(t, app.logger)
	assert.Nil(t, app.store, "store should be nil without .mtix")
}

// TestInitApp_DebugLogLevel_SetsDebug verifies debug log level.
func TestInitApp_DebugLogLevel_SetsDebug(t *testing.T) {
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	old := app
	defer func() { app = old }()
	app = appContext{}

	cmd := &cobra.Command{Use: "test"}
	err = initApp(cmd, "debug")
	assert.NoError(t, err)
	assert.NotNil(t, app.logger)
}

// TestInitApp_WarnLogLevel_SetsWarn verifies warn log level.
func TestInitApp_WarnLogLevel_SetsWarn(t *testing.T) {
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	old := app
	defer func() { app = old }()
	app = appContext{}

	cmd := &cobra.Command{Use: "test"}
	err = initApp(cmd, "warn")
	assert.NoError(t, err)
}

// TestInitApp_ErrorLogLevel_SetsError verifies error log level.
func TestInitApp_ErrorLogLevel_SetsError(t *testing.T) {
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	old := app
	defer func() { app = old }()
	app = appContext{}

	cmd := &cobra.Command{Use: "test"}
	err = initApp(cmd, "error")
	assert.NoError(t, err)
}

// TestInitApp_WithMtixDir_InitializesStoreAndServices verifies full init.
func TestInitApp_WithMtixDir_InitializesStoreAndServices(t *testing.T) {
	tmpDir := t.TempDir()
	mtixDir := filepath.Join(tmpDir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))

	// Create a minimal config.yaml.
	configContent := "prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(mtixDir, "config.yaml"), []byte(configContent), 0o644,
	))

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	old := app
	defer func() {
		if app.store != nil {
			_ = app.store.Close()
		}
		app = old
	}()
	app = appContext{}

	cmd := &cobra.Command{Use: "test"}
	err = initApp(cmd, "")
	assert.NoError(t, err)
	assert.NotNil(t, app.store, "store should be initialized")
	assert.NotNil(t, app.nodeSvc, "nodeSvc should be initialized")
	assert.NotNil(t, app.ctxSvc, "ctxSvc should be initialized")
	assert.NotNil(t, app.promptSvc, "promptSvc should be initialized")
	assert.NotNil(t, app.agentSvc, "agentSvc should be initialized")
	assert.NotNil(t, app.sessionSvc, "sessionSvc should be initialized")
	assert.NotNil(t, app.bgSvc, "bgSvc should be initialized")
	assert.NotNil(t, app.configSvc, "configSvc should be initialized")
	assert.NotNil(t, app.syncSvc, "syncSvc should be initialized")
	assert.NotEmpty(t, app.mtixDir, "mtixDir should be set")
}

// TestInitApp_FreshClone_NoDataDir_CreatesDBAndAutoImports verifies FR-15.2a:
// when .mtix/ exists with config.yaml and tasks.json but no data/ directory
// (fresh clone scenario), initApp creates the data dir and opens the store.
func TestInitApp_FreshClone_NoDataDir_CreatesDBAndAutoImports(t *testing.T) {
	tmpDir := t.TempDir()
	mtixDir := filepath.Join(tmpDir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))

	// Write config.yaml (git-tracked).
	configContent := "prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(mtixDir, "config.yaml"), []byte(configContent), 0o644,
	))

	// Do NOT create .mtix/data/ — simulates fresh clone where data/ is gitignored.

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	old := app
	defer func() {
		if app.store != nil {
			_ = app.store.Close()
		}
		app = old
	}()
	app = appContext{}

	cmd := &cobra.Command{Use: "test"}
	err = initApp(cmd, "")
	require.NoError(t, err)
	assert.NotNil(t, app.store, "store should be initialized even without pre-existing data dir")
	assert.NotNil(t, app.syncSvc, "syncSvc should be initialized")
	assert.Contains(t, app.mtixDir, ".mtix", "mtixDir should be set")

	// Verify .mtix/data/ directory was created.
	info, err := os.Stat(filepath.Join(mtixDir, "data"))
	require.NoError(t, err, ".mtix/data directory should be created")
	assert.True(t, info.IsDir(), ".mtix/data should be a directory")

	// Verify database file exists inside data/.
	_, err = os.Stat(filepath.Join(mtixDir, "data", "mtix.db"))
	assert.NoError(t, err, "mtix.db should exist inside data/")
}

// TestShouldSkipAutoImport_ExcludedCommands verifies FR-15.2c:
// auto-import skips for init, export, import, help, version, migrate commands.
func TestShouldSkipAutoImport_ExcludedCommands(t *testing.T) {
	tests := []struct {
		name     string
		cmdName  string
		wantSkip bool
	}{
		{"init skips", "init", true},
		{"export skips", "export", true},
		{"import skips", "import", true},
		{"help skips", "help", true},
		{"version skips", "version", true},
		{"migrate skips", "migrate", true},
		{"show does not skip", "show", false},
		{"list does not skip", "list", false},
		{"create does not skip", "create", false},
		{"update does not skip", "update", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSkipAutoImport(tt.cmdName)
			assert.Equal(t, tt.wantSkip, got)
		})
	}
}

// TestIsMutationCommand verifies FR-15.3 mutation command list.
func TestIsMutationCommand(t *testing.T) {
	tests := []struct {
		name       string
		cmdName    string
		wantMutate bool
	}{
		{"create is mutation", "create", true},
		{"update is mutation", "update", true},
		{"done is mutation", "done", true},
		{"cancel is mutation", "cancel", true},
		{"decompose is mutation", "decompose", true},
		{"import is mutation", "import", true},
		{"show is not mutation", "show", false},
		{"list is not mutation", "list", false},
		{"tree is not mutation", "tree", false},
		{"search is not mutation", "search", false},
		{"stats is not mutation", "stats", false},
		{"export is not mutation", "export", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMutationCommand(tt.cmdName)
			assert.Equal(t, tt.wantMutate, got)
		})
	}
}

// TestCloseApp_WithStore_ClosesSuccessfully verifies store cleanup.
func TestCloseApp_WithStore_ClosesSuccessfully(t *testing.T) {
	tmpDir := t.TempDir()
	mtixDir := filepath.Join(tmpDir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))

	configContent := "prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(mtixDir, "config.yaml"), []byte(configContent), 0o644,
	))

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	old := app
	defer func() { app = old; resetCloseOnce() }()
	app = appContext{}
	resetCloseOnce()

	cmd := &cobra.Command{Use: "test"}
	require.NoError(t, initApp(cmd, ""))
	require.NotNil(t, app.store)

	err = closeApp()
	assert.NoError(t, err)
}

// TestRunInit_ValidPrefix_InCleanDir_Succeeds verifies successful init.
func TestRunInit_ValidPrefix_InCleanDir_Succeeds(t *testing.T) {
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	old := app
	defer func() { app = old }()
	app = appContext{}

	err = runInit("TEST")
	assert.NoError(t, err)

	// Verify .mtix directory was created.
	info, err := os.Stat(filepath.Join(tmpDir, ".mtix"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Verify config.yaml was created.
	_, err = os.Stat(filepath.Join(tmpDir, ".mtix", "config.yaml"))
	assert.NoError(t, err)
}

// TestRunInit_AlreadyInitialized_ReturnsError verifies re-init detection.
func TestRunInit_AlreadyInitialized_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".mtix"), 0o755))

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	err = runInit("TEST")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already initialized")
}

// TestRun_NoArgs_Succeeds verifies run() with help displays.
func TestRun_NoArgs_Succeeds(t *testing.T) {
	// Override os.Args to prevent side effects.
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"mtix", "--help"}

	err := run()
	assert.NoError(t, err)
}

// TestNewRootCmd_PersistentPreRunE_VersionCmd_Skips verifies version skips init.
func TestNewRootCmd_PersistentPreRunE_VersionCmd_Skips(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	rootCmd := newRootCmd()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"--version"})
	err := rootCmd.Execute()
	assert.NoError(t, err)
}

// TestNewRootCmd_PersistentPreRunE_HelpCmd_Skips verifies help skips init.
func TestNewRootCmd_PersistentPreRunE_HelpCmd_Skips(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	rootCmd := newRootCmd()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"help"})
	err := rootCmd.Execute()
	assert.NoError(t, err)
}

// TestShouldRouteToServer_WithLivePID_ReturnsPort verifies routing to live server.
func TestShouldRouteToServer_WithLivePID_ReturnsPort(t *testing.T) {
	tmpDir := t.TempDir()
	mtixDir := filepath.Join(tmpDir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))

	// Write lock with current PID (guaranteed alive).
	lockContent := fmt.Sprintf("%d\n8377\n", os.Getpid())
	lockPath := filepath.Join(mtixDir, pidLockFile)
	require.NoError(t, os.WriteFile(lockPath, []byte(lockContent), 0o600))

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	cmd := &cobra.Command{Use: "show"}
	port := shouldRouteToServer(cmd)
	assert.Equal(t, 8377, port)
}

// TestNewRootCmd_MigrateSubcommand_Succeeds verifies migrate runs through root.
func TestNewRootCmd_MigrateSubcommand_Succeeds(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	rootCmd := newRootCmd()
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"migrate"})

	// migrate is exempt from routing and init, should succeed.
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	err = rootCmd.Execute()
	assert.NoError(t, err)
}
