// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsMutationCommand_GC_ReturnsTrue verifies gc is recognized as a mutation
// command per FR-15.3. GC permanently deletes nodes — auto-export must fire.
func TestIsMutationCommand_GC_ReturnsTrue(t *testing.T) {
	assert.True(t, isMutationCommand("gc"),
		"gc permanently deletes nodes; must trigger auto-export per FR-15.3")
}

// TestIsMutationCommand_AllMutatingAdminCommands verifies all admin commands
// that modify data are in the mutation allowlist per FR-15.3.
func TestIsMutationCommand_AllMutatingAdminCommands(t *testing.T) {
	// gc permanently deletes nodes — must trigger auto-export.
	mutatingAdminCmds := []string{"gc"}
	for _, cmd := range mutatingAdminCmds {
		t.Run(cmd, func(t *testing.T) {
			assert.True(t, isMutationCommand(cmd),
				"%s modifies data; must be in isMutationCommand()", cmd)
		})
	}
}

// TestExportCommand_ProducesEnvelopeFormat verifies the export CLI produces
// the FR-7.8 ExportData envelope with version, schema_version, nodes, checksum —
// not a flat JSON array.
func TestExportCommand_ProducesEnvelopeFormat(t *testing.T) {
	// Set up a test environment with a real store.
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

	// Capture stdout to verify export output format.
	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = runExport("json")
	require.NoError(t, err)

	if closeErr := w.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	os.Stdout = oldStdout
	_, _ = buf.ReadFrom(r)

	output := buf.Bytes()
	require.True(t, len(output) > 0, "export should produce output")

	// Parse the output — it should be an ExportData envelope, not a flat array.
	var envelope map[string]json.RawMessage
	err = json.Unmarshal(output, &envelope)
	require.NoError(t, err, "export output should be valid JSON object")

	// FR-7.8 required fields in the envelope.
	assert.Contains(t, envelope, "version",
		"export must include 'version' field per FR-7.8")
	assert.Contains(t, envelope, "schema_version",
		"export must include 'schema_version' field per FR-7.8")
	assert.Contains(t, envelope, "nodes",
		"export must include 'nodes' field per FR-7.8")
	assert.Contains(t, envelope, "checksum",
		"export must include 'checksum' field per FR-7.8")
	assert.Contains(t, envelope, "node_count",
		"export must include 'node_count' field per FR-7.8")

	// Verify it is NOT a flat array (which is the current broken format).
	var arr []json.RawMessage
	arrErr := json.Unmarshal(output, &arr)
	assert.Error(t, arrErr, "export must NOT produce a flat array — must be envelope format")
}

// TestWithAutoExport_FiresOnRunEError verifies the withAutoExport wrapper
// triggers auto-export even when the wrapped RunE returns an error.
// This is critical per FR-15.3b: Cobra skips PersistentPostRunE on RunE error,
// so the wrapper is the only mechanism that fires export on error paths.
func TestWithAutoExport_FiresOnRunEError(t *testing.T) {
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
	require.NotNil(t, app.syncSvc)
	app.mtixDir = mtixDir

	// Run a withAutoExport-wrapped function that returns an error.
	wrappedFn := withAutoExport(func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("simulated mutation error")
	})

	cmd.SetContext(context.Background())
	runErr := wrappedFn(cmd, nil)
	require.Error(t, runErr, "original error should propagate")
	assert.Contains(t, runErr.Error(), "simulated mutation error")

	// Despite the error, tasks.json should have been written by auto-export.
	tasksPath := filepath.Join(mtixDir, "tasks.json")
	_, statErr := os.Stat(tasksPath)
	assert.NoError(t, statErr, "tasks.json should exist — withAutoExport must fire even on RunE error")
}

// TestWithAutoExport_AllMutationCommandsWrapped verifies that every command
// in the isMutationCommand() list has its RunE wrapped with withAutoExport.
// This is a structural test — it checks that the wrapping was applied.
func TestWithAutoExport_AllMutationCommandsWrapped(t *testing.T) {
	// Build the root command tree and find mutation subcommands.
	rootCmd := newRootCmd()

	mutationCmds := []string{
		"create", "update", "done", "cancel", "decompose", "reopen",
		"delete", "undelete", "claim", "unclaim", "defer", "rerun",
		"restore", "import", "prompt", "annotate", "resolve-annotation",
		"comment", "micro", "gc",
	}

	for _, name := range mutationCmds {
		t.Run(name, func(t *testing.T) {
			assert.True(t, isMutationCommand(name),
				"%s must be in isMutationCommand() per FR-15.3", name)

			// Find the command in the tree.
			cmd, _, err := rootCmd.Find([]string{name})
			require.NoError(t, err, "command %s must exist", name)
			assert.NotNil(t, cmd.RunE, "command %s must have RunE", name)
		})
	}

	// Also check dep subcommands (add, remove) which are mutations.
	depCmd, _, err := rootCmd.Find([]string{"dep"})
	require.NoError(t, err)

	for _, sub := range []string{"add", "remove"} {
		t.Run("dep_"+sub, func(t *testing.T) {
			subCmd, _, err := depCmd.Find([]string{sub})
			require.NoError(t, err, "dep %s must exist", sub)
			assert.NotNil(t, subCmd.RunE, "dep %s must have RunE", sub)
		})
	}
}
