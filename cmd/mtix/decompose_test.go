// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecomposeCmd_FileFlag_Exists(t *testing.T) {
	cmd := newDecomposeCmd()
	f := cmd.Flags().Lookup("file")
	require.NotNil(t, f, "--file flag should exist")
	assert.Equal(t, "f", f.Shorthand)
}

func TestDecomposeCmd_ArgsOrFile_Required(t *testing.T) {
	// With no args and no file, the command should require at least a parent ID.
	cmd := newDecomposeCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.Error(t, err)
}

func TestParseJSONLFile_ValidFile_ParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	planFile := filepath.Join(dir, "plan.jsonl")

	content := `{"title":"Task A","prompt":"Do A"}
{"title":"Task B","prompt":"Do B","priority":1}
`
	require.NoError(t, os.WriteFile(planFile, []byte(content), 0o644))

	inputs, err := parseJSONLFile(planFile)
	require.NoError(t, err)
	require.Len(t, inputs, 2)
	assert.Equal(t, "Task A", inputs[0].Title)
	assert.Equal(t, "Do A", inputs[0].Prompt)
	assert.Equal(t, "Task B", inputs[1].Title)
}

func TestParseJSONLFile_NonexistentFile_ReturnsError(t *testing.T) {
	_, err := parseJSONLFile("/nonexistent/plan.jsonl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open plan file")
}

func TestDecomposeCmd_DryRunFlag_Exists(t *testing.T) {
	cmd := newDecomposeCmd()
	f := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, f, "--dry-run flag should exist")
	assert.Equal(t, "n", f.Shorthand)
	assert.Equal(t, "false", f.DefValue)
}

func TestDecomposeCmd_DryRunFlag_InCmdConstructionTest(t *testing.T) {
	cmd := newDecomposeCmd()
	// --dry-run and --file should coexist
	dryRun := cmd.Flags().Lookup("dry-run")
	file := cmd.Flags().Lookup("file")
	require.NotNil(t, dryRun)
	require.NotNil(t, file)
}
