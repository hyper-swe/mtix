// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package ownership follows the real PR commands, including the SQLite target
// that no longer consumes the SQLite manifest (MTIX-112.17.29.1).
package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func prPackageInventory(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	var packages []string
	for _, pkg := range strings.Fields(string(out)) {
		if !strings.Contains(pkg, "/node_modules/") {
			packages = append(packages, pkg)
		}
	}
	require.NotEmpty(t, packages)
	sort.Strings(packages)
	return packages
}

func prPackageReplica(t *testing.T, inventory []string) string {
	t.Helper()
	manifests := map[string][]string{}
	for _, name := range []string{"cli", "other", "sqlite"} {
		raw, err := os.ReadFile(filepath.Join("ci-go-shards", name+".txt"))
		require.NoError(t, err)
		manifests[name] = strings.Fields(string(raw))
	}
	root := shardFixture(t, inventory, manifests)
	for _, dir := range []string{"internal/store/sqlite", "cmd/mtix"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o750))
	}
	for _, name := range []string{"cmd-mtix-shards.sh"} {
		raw, err := os.ReadFile(name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", name), raw, 0o600))
	}
	shim := `#!/usr/bin/env bash
set -eu
case "$1" in
list) [ "$*" = 'list ./...' ] || exit 99; cat "$SHARD_TEST_LIST" ;;
test)
  shift
  if [ "$1" = -list ]; then
    printf 'TestCoverage1\nTestCoverage2\nTestCoverage3\nTestCoverage4\nTestCoverage5\nTestCoverage6\n'
    exit 0
  fi
  for package in "$@"; do
    case "$package" in -*) break ;; esac
    printf '%s\n' "$package" >> "$PR_PACKAGE_LOG"
  done ;;
*) exit 99 ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin", "go"), []byte(shim), 0o750)) //nolint:gosec // executable shim in a private fixture
	return root
}

func prJobPackages(t *testing.T, root string, job ciGoJob, shard, pkg string) []string {
	t.Helper()
	var commands []string
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "go test") || strings.Contains(step.Run, "shards.sh") {
			commands = append(commands, step.Run)
		}
	}
	require.NotEmpty(t, commands, "job must actually run package tests")
	code := strings.Join(commands, "\n")
	code = strings.ReplaceAll(code, "${{ matrix.pkg }}", pkg)
	code = strings.ReplaceAll(code, "${{ matrix.verbose }}", "")
	log := filepath.Join(root, "selected-packages.txt")
	require.NoError(t, os.WriteFile(log, nil, 0o600))
	cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", code)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SHARD="+shard, "SHARD_TEST_LIST="+filepath.Join(root, "packages.txt"), "PR_PACKAGE_LOG="+log)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	raw, err := os.ReadFile(log) //nolint:gosec // test-owned recorder
	require.NoError(t, err)
	selected := strings.Fields(string(raw))
	require.NotEmpty(t, selected)
	return selected
}

func expandedPRPackages(t *testing.T, selected, inventory []string) []string {
	t.Helper()
	var expanded []string
	for _, pkg := range selected {
		if pkg == "." {
			pkg = shardModule
		} else if strings.HasPrefix(pkg, "./") {
			pkg = shardModule + "/" + strings.TrimPrefix(pkg, "./")
		}
		if strings.HasSuffix(pkg, "/...") {
			prefix := strings.TrimSuffix(pkg, "/...")
			found := false
			for _, actual := range inventory {
				if actual == prefix || strings.HasPrefix(actual, prefix+"/") {
					expanded = append(expanded, actual)
					found = true
				}
			}
			require.True(t, found, "package pattern %s must select real packages", pkg)
		} else {
			expanded = append(expanded, strings.TrimSuffix(pkg, "/"))
		}
	}
	return expanded
}

func requirePackageOwners(t *testing.T, inventory []string, owners map[string][]string) {
	t.Helper()
	actual := make([]string, 0, len(owners))
	for pkg, jobs := range owners {
		actual = append(actual, pkg)
		require.Len(t, jobs, 1, "%s has multiple PR job owners: %v", pkg, jobs)
	}
	sort.Strings(actual)
	require.Equal(t, inventory, actual, "PR job ownership must cover every package")
}

func requirePRPackageOwnership(t *testing.T) {
	t.Helper()
	inventory := prPackageInventory(t)
	root, jobs := prPackageReplica(t, inventory), readCIJobs(t)
	ordinary, sqlite, cmdJob := jobs["test-go-shards"], jobs["test-go-sqlite"], jobs["test-go-cmd"]
	for _, platform := range []string{"ubuntu-22.04", "macos-latest"} {
		t.Run(platform, func(t *testing.T) {
			require.Contains(t, ordinary.Strategy.Matrix.OS, platform)
			require.Contains(t, sqlite.Strategy.Matrix.OS, platform)
			require.Contains(t, cmdJob.Strategy.Matrix.OS, platform)
			owners := map[string][]string{}
			for _, shard := range ordinary.Strategy.Matrix.Shard {
				for _, pkg := range expandedPRPackages(t, prJobPackages(t, root, ordinary, shard, ""), inventory) {
					owners[pkg] = append(owners[pkg], "ordinary/"+shard)
				}
			}
			// SQLite test shards share one package owner. Existing compiled-name
			// tests separately prove every test belongs to exactly one shard.
			require.NotEmpty(t, sqlite.Strategy.Matrix.Shard)
			for _, shard := range sqlite.Strategy.Matrix.Shard {
				selected := expandedPRPackages(t, prJobPackages(t, root, sqlite, shard, ""), inventory)
				require.Equal(t, []string{shardModule + "/internal/store/sqlite"}, selected)
			}
			owners[shardModule+"/internal/store/sqlite"] = append(owners[shardModule+"/internal/store/sqlite"], "sqlite")
			// MTIX-112.17.22.1: cmd/mtix has one owner, the test-go-cmd matrix;
			// cmd-mtix-shards.sh check proves each test is in exactly one shard.
			require.NotEmpty(t, cmdJob.Strategy.Matrix.Shard)
			for _, shard := range cmdJob.Strategy.Matrix.Shard {
				selected := expandedPRPackages(t, prJobPackages(t, root, cmdJob, shard, ""), inventory)
				require.Equal(t, []string{shardModule + "/cmd/mtix"}, selected)
			}
			owners[shardModule+"/cmd/mtix"] = append(owners[shardModule+"/cmd/mtix"], "cmd")
			requirePackageOwners(t, inventory, owners)
		})
	}
	requirePostgresPackageOwners(t, root, jobs, inventory)
}

// PG coverage intentionally repeats a subset from the ordinary partition.
func requirePostgresPackageOwners(t *testing.T, root string, jobs map[string]ciGoJob, inventory []string) {
	t.Helper()
	cmd, docker := jobs["test-go-postgres-cmd"], jobs["test-go-postgres-docker"]
	expected := expandedPRPackages(t, []string{"./cmd/mtix/", "./internal/store/postgres/transport/", "./e2e/..."}, inventory)
	sort.Strings(expected)
	for _, version := range []string{"16", "17"} {
		t.Run("postgres-"+version, func(t *testing.T) {
			require.Contains(t, cmd.Strategy.Matrix.PG, version)
			require.Contains(t, docker.Strategy.Matrix.PG, version)
			require.NotEmpty(t, cmd.Strategy.Matrix.Shard)
			for _, shard := range cmd.Strategy.Matrix.Shard {
				selected := expandedPRPackages(t, prJobPackages(t, root, cmd, shard, ""), inventory)
				require.Equal(t, []string{shardCmdMtix}, selected)
			}
			owners := map[string][]string{shardCmdMtix: {"postgres-cmd"}}
			for _, target := range docker.Strategy.Matrix.Pkg {
				for _, pkg := range expandedPRPackages(t, prJobPackages(t, root, docker, "", target), inventory) {
					owners[pkg] = append(owners[pkg], "postgres/"+target)
				}
			}
			requirePackageOwners(t, expected, owners)
		})
	}
}
