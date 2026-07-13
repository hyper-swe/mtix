// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-56.4: global -C/--project-dir. Any command can target a named project
// directory without cd — git -C semantics (an effective working-directory
// change applied before store init), so relative paths in hook configs and
// mirrors resolve against the target project. `--project` keeps meaning
// project PREFIX everywhere it exists today; the one prior exception (`mcp
// --project` = directory) becomes a deprecated alias of the global flag.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCmd_GlobalProjectDirFlag(t *testing.T) {
	root := newRootCmd()
	f := root.PersistentFlags().Lookup("project-dir")
	require.NotNil(t, f, "global --project-dir declared on the root command")
	assert.Equal(t, "C", f.Shorthand, "-C shorthand, matching git -C")
}

func TestGlobalProjectDir_ChangesWorkingDirectoryBeforeInit(t *testing.T) {
	orig, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(orig)) })

	dir := t.TempDir()
	root := newRootCmd()
	root.SetArgs([]string{"-C", dir, "help"})
	require.NoError(t, root.Execute())

	got, err := os.Getwd()
	require.NoError(t, err)
	// macOS tempdirs live behind /private symlinks; compare resolved paths.
	wantResolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotResolved, err := filepath.EvalSymlinks(got)
	require.NoError(t, err)
	assert.Equal(t, wantResolved, gotResolved,
		"-C must take effect before any command logic (even skip-list commands like help)")
}

func TestGlobalProjectDir_BadDirFailsLoudly(t *testing.T) {
	orig, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(orig)) })

	root := newRootCmd()
	root.SetArgs([]string{"-C", filepath.Join(t.TempDir(), "does-not-exist"), "help"})
	err = root.Execute()
	require.Error(t, err, "a bad --project-dir is an explicit error, not a silent cwd fallback")
	require.Contains(t, err.Error(), "project-dir")
}

func TestMCPCmd_ProjectFlagIsDeprecatedAlias(t *testing.T) {
	cmd := newMCPCmd()
	f := cmd.Flags().Lookup("project")
	require.NotNil(t, f, "mcp --project stays for one release (back-compat)")
	assert.NotEmpty(t, f.Deprecated, "marked deprecated, pointing at the global flag")
	assert.Empty(t, f.Shorthand,
		"the -C shorthand moved to the global persistent flag (same meaning, same spelling: 'mtix mcp -C dir' keeps working)")
}
