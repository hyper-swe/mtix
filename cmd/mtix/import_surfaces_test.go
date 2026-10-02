// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImportSurfaces_OnlyTheReconcilingPathsMerge verifies that, outside the
// store, the only production code that runs a merge import is the CLI
// (mtix import, which refuses workflow conflicts and takes --prefer,
// --theirs and --ours) and the relay bootstrap (which passes its options to
// the same reconcile path, so it refuses too); the REST, gRPC and MCP
// handlers have no import at all, and the automatic import runs replace
// mode behind its own loss check (MTIX-95.31.13). A new caller must be
// added here deliberately, after it handles workflow conflicts.
func TestImportSurfaces_OnlyTheReconcilingPathsMerge(t *testing.T) {
	root := filepath.Join("..", "..")
	var callers []string
	walkErr := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			strings.Contains(path, filepath.Join("internal", "store", "sqlite")) {
			return nil
		}
		src, readErr := os.ReadFile(path) //nolint:gosec // a source file of this repository
		if readErr != nil {
			return readErr
		}
		text := string(src)
		if strings.Contains(text, "ImportModeMerge") || strings.Contains(text, ".ImportReconcile(") ||
			strings.Contains(text, "ImportReconcileOptions") {
			callers = append(callers, filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator))))
		}
		return nil
	})
	require.NoError(t, walkErr)
	sort.Strings(callers)
	assert.Equal(t, []string{"internal/relay/bootstrap/bootstrap.go"}, callers)

	src, err := os.ReadFile("admin.go")
	require.NoError(t, err)
	assert.Contains(t, string(src), "ImportReconcile(", "the CLI merges through ImportReconcile only")
	assert.NotContains(t, string(src), "app.store.Import(", "the CLI never calls the unreconciled Import")
}
