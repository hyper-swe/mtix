// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A balanced manifest partition can still lose a package from the actual jobs.
// Run the real guard tests in a private source inventory to prove both checks
// detect that gap independently, rather than depending on each other's failure.
func TestGoWorkflow_PackageOwnership_RejectsSQLiteManifestMove(t *testing.T) {
	inventory := prPackageInventory(t)
	for _, moveTo := range []string{"", "sqlite", "cli"} {
		t.Run("move-to-"+moveTo, func(t *testing.T) {
			root := prPackageReplica(t, inventory)
			workflow, err := os.ReadFile("../.github/workflows/ci.yml")
			require.NoError(t, err)
			dir := filepath.Join(root, ".github", "workflows")
			require.NoError(t, os.MkdirAll(dir, 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ci.yml"), workflow, 0o600))
			if moveTo != "" {
				moveAPIPackage(t, root, moveTo)
			}
			// The original generic partition check accepts all three cases.
			out, err := runShardGuard(root, "check")
			require.NoError(t, err, out)
			binary, err := os.Executable()
			require.NoError(t, err)
			cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestGoWorkflow_(SQLiteManifest_ContainsOnlySQLite|PackageOwnership_CoversEveryPackage)$", "-test.count=1")
			cmd.Dir = filepath.Join(root, "scripts")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SHARD_TEST_LIST="+filepath.Join(root, "packages.txt"))
			output, err := cmd.CombinedOutput()
			if moveTo != "sqlite" {
				require.NoError(t, err, string(output))
				return
			}
			require.Error(t, err, string(output))
			require.Contains(t, string(output), "--- FAIL: TestGoWorkflow_SQLiteManifest_ContainsOnlySQLite")
			require.Contains(t, string(output), "--- FAIL: TestGoWorkflow_PackageOwnership_CoversEveryPackage")
			require.Contains(t, string(output), shardModule+"/internal/api/http")
		})
	}
}

func moveAPIPackage(t *testing.T, root, target string) {
	t.Helper()
	api := shardModule + "/internal/api/http\n"
	other := filepath.Join(root, "scripts", "ci-go-shards", "other.txt")
	raw, err := os.ReadFile(other)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(raw), api))
	require.NoError(t, os.WriteFile(other, []byte(strings.Replace(string(raw), api, "", 1)), 0o600))
	path := filepath.Join(root, "scripts", "ci-go-shards", target+".txt")
	raw, err = os.ReadFile(path) //nolint:gosec // test-owned manifest
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(raw, []byte(api)...), 0o600))
}
