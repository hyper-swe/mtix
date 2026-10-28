// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// The named real-tree guard checks a private bad source inventory in a child
// process, so an accidentally empty guard cannot silently pass.
import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

func requireFreshFixtureRoutesRejectsImage(t *testing.T) {
	t.Helper()
	dir := copyFreshFixtureRouteSources(t)
	binary, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestFreshFixtureRoutes_RealTree_UseFreshConstructor$", "-test.count=1")
	cmd.Dir = dir
	cmd.Env = append(hostguard.Environ(), "MTIX_TEST_FRESH_ROUTES_CHILD=1")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "fresh fixture guard accepted an image-backed creation route: %s", out)
	require.Contains(t, string(out), "fresh fixture routing:")
	require.Contains(t, string(out), "TestNew_CreatesDB")
	require.Contains(t, string(out), "image")
}

func copyFreshFixtureRouteSources(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	mutated := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(entry.Name())
		require.NoError(t, err)
		if entry.Name() == "store_test.go" {
			before := "func TestNew_CreatesDB(t *testing.T) {\n\ts := newFreshTestStore(t)"
			after := "func TestNew_CreatesDB(t *testing.T) {\n\ts := newTestStore(t)"
			require.Equal(t, 1, strings.Count(string(raw), before))
			raw = []byte(strings.Replace(string(raw), before, after, 1))
			mutated = true
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, entry.Name()), raw, 0o600))
	}
	require.True(t, mutated, "creation route source was not found")
	return dir
}
