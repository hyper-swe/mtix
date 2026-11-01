// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cleanupRecipeCase struct {
	name, exportExit, existing string
	success                    bool
}

type cleanupRecipeFixture struct {
	dir, bin, board, temporary, protected string
}

func cleanupExportRecipe(t *testing.T, path string) string {
	t.Helper()
	var recipes []string
	for _, block := range fencedBlockRE.FindAllStringSubmatch(cleanupCommandSection(t, path), -1) {
		if strings.Contains(block[2], "mtix export") {
			recipes = append(recipes, block[2])
		}
	}
	require.Len(t, recipes, 1, "%s needs one export recipe", path)
	return recipes[0]
}

func newCleanupRecipeFixture(t *testing.T, existing string) cleanupRecipeFixture {
	t.Helper()
	dir := t.TempDir()
	f := cleanupRecipeFixture{dir: dir, bin: filepath.Join(dir, "bin"), board: filepath.Join(dir, ".mtix", "tasks.json"), temporary: filepath.Join(dir, ".mtix", "tasks-clean.json"), protected: filepath.Join(dir, "protected")}
	require.NoError(t, os.MkdirAll(filepath.Dir(f.board), 0o700))
	require.NoError(t, os.Mkdir(f.bin, 0o700))
	writeCleanupRecipeFile(t, f.board, "original-snapshot\n", 0o600)
	writeCleanupRecipeFile(t, f.protected, "protected\n", 0o600)
	switch existing {
	case "file":
		writeCleanupRecipeFile(t, f.temporary, "occupied\n", 0o600)
	case "link":
		require.NoError(t, os.Symlink(f.protected, f.temporary))
	}
	stub := "#!/bin/sh\n[ \"$1\" = export ] || exit 2\nprintf '%s\\n' clean-snapshot\nexit \"$CLEANUP_RECIPE_EXIT\"\n"
	writeCleanupRecipeFile(t, filepath.Join(f.bin, "mtix"), stub, 0o700)
	return f
}

func writeCleanupRecipeFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(data), mode)) //nolint:gosec // controlled paths inside t.TempDir
}

func runCleanupRecipe(t *testing.T, recipe string, tc cleanupRecipeCase, f cleanupRecipeFixture) (string, error) {
	t.Helper()
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// Force a permissive outer umask so a missing recipe umask cannot hide.
	cmd := exec.CommandContext(ctx, shell, "-c", "umask 022\n"+recipe) //nolint:gosec // reviewed documentation recipe, private fixture and mock mtix
	cmd.Dir = f.dir
	cmd.Env = append(os.Environ(), "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CLEANUP_RECIPE_EXIT="+tc.exportExit)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	require.NoError(t, ctx.Err(), "recipe must finish")
	require.Empty(t, stdout.String(), "exported data must not reach the terminal")
	return stderr.String(), runErr
}

func assertCleanupRecipeResult(t *testing.T, tc cleanupRecipeCase, f cleanupRecipeFixture, runErr error, stderr string) {
	t.Helper()
	board, err := os.ReadFile(f.board) //nolint:gosec // controlled fixture path
	require.NoError(t, err)
	if tc.success {
		require.NoError(t, runErr, stderr)
		require.Equal(t, "clean-snapshot\n", string(board))
		info, statErr := os.Lstat(f.board)
		require.NoError(t, statErr)
		require.True(t, info.Mode().IsRegular())
		require.Zero(t, info.Mode().Perm()&0o077, "new snapshot must be private")
		_, statErr = os.Lstat(f.temporary)
		require.True(t, os.IsNotExist(statErr))
	} else {
		require.Error(t, runErr)
		require.Equal(t, "original-snapshot\n", string(board))
		protected, readErr := os.ReadFile(f.protected) //nolint:gosec // controlled fixture path
		require.NoError(t, readErr)
		require.Equal(t, "protected\n", string(protected))
		if tc.existing == "file" {
			temporary, readErr := os.ReadFile(f.temporary) //nolint:gosec // controlled fixture path
			require.NoError(t, readErr)
			require.Equal(t, "occupied\n", string(temporary))
		}
	}
}

func TestHistorySensitiveTextCommands_RecipeSafety(t *testing.T) {
	for _, path := range []string{"USERMANUAL.md", "internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md"} {
		t.Run(path, func(t *testing.T) {
			recipe := cleanupExportRecipe(t, path)
			for _, tc := range []cleanupRecipeCase{{"success", "0", "", true}, {"failed_export", "1", "", false}, {"existing_temporary", "0", "file", false}, {"existing_symlink", "0", "link", false}} {
				t.Run(tc.name, func(t *testing.T) {
					f := newCleanupRecipeFixture(t, tc.existing)
					stderr, err := runCleanupRecipe(t, recipe, tc, f)
					assertCleanupRecipeResult(t, tc, f, err, stderr)
				})
			}
		})
	}
}
