// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.17.22: explicit CI shards must cover every selected package exactly
// once. These tests run the real guard in private replicas with a fake go list.
package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const shardModule = "github.com/hyper-swe/mtix"

func shardManifests() map[string][]string {
	return map[string][]string{
		"sqlite": {shardModule + "/internal/store/sqlite"},
		"cli":    {shardModule + "/cmd/mtix"},
		"other":  {shardModule},
	}
}

func shardFixture(t *testing.T, packages []string, manifests map[string][]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "scripts", "ci-go-shards")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	source, err := os.ReadFile("ci-go-shards.sh")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "ci-go-shards.sh"), source, 0o600))
	for name, entries := range manifests {
		text := strings.Join(entries, "\n")
		if len(entries) > 0 {
			text += "\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".txt"), []byte(text), 0o600))
	}
	require.NoError(t, os.Mkdir(filepath.Join(root, "bin"), 0o750))
	shim := "#!/usr/bin/env bash\n[ \"$*\" = 'list ./...' ] || exit 99\ncat \"$SHARD_TEST_LIST\"\nexit \"${SHARD_TEST_EXIT:-0}\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin", "go"), []byte(shim), 0o750))
	list := strings.Join(packages, "\n")
	if len(packages) > 0 {
		list += "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "packages.txt"), []byte(list), 0o600))
	return root
}

func runShardGuard(root string, args ...string) (string, error) {
	cmd := exec.Command("bash", append([]string{filepath.Join(root, "scripts", "ci-go-shards.sh")}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SHARD_TEST_LIST="+filepath.Join(root, "packages.txt"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestGoShards_UnlistedPackage_Fails(t *testing.T) {
	packages := []string{shardModule, shardModule + "/cmd/mtix", shardModule + "/internal/store/sqlite", shardModule + "/newpackage"}
	root := shardFixture(t, packages, shardManifests())
	out, err := runShardGuard(root, "check")
	require.Error(t, err, "the guard accepted an unlisted package: %s", out)
	require.Contains(t, out, "unlisted packages")
	require.Contains(t, out, shardModule+"/newpackage")
	path := filepath.Join(root, "scripts", "ci-go-shards", "other.txt")
	require.NoError(t, os.WriteFile(path, []byte(shardModule+"\n"+shardModule+"/newpackage\n"), 0o600))
	out, err = runShardGuard(root, "check")
	require.NoError(t, err, out)
}

func TestGoShards_InvalidPartition_Fails(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string][]string)
		want   string
	}{
		{"duplicate within shard", func(m map[string][]string) { m["other"] = append(m["other"], shardModule) }, "duplicate packages"},
		{"duplicate across shards", func(m map[string][]string) { m["other"] = append(m["other"], m["cli"]...) }, "duplicate packages"},
		{"stale package", func(m map[string][]string) { m["other"] = append(m["other"], shardModule+"/removed") }, "stale packages"},
		{"empty shard", func(m map[string][]string) { m["sqlite"] = nil }, "missing or empty manifest"},
		{"missing shard", func(m map[string][]string) { delete(m, "cli") }, "missing or empty manifest"},
		{"blank entry", func(m map[string][]string) { m["other"] = append(m["other"], "") }, "stale packages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifests := shardManifests()
			tc.change(manifests)
			packages := []string{shardModule, shardModule + "/cmd/mtix", shardModule + "/internal/store/sqlite"}
			root := shardFixture(t, packages, manifests)
			out, err := runShardGuard(root, "check")
			require.Error(t, err, out)
			require.Contains(t, out, tc.want)
		})
	}
}

func TestGoShards_InvalidInventory_Fails(t *testing.T) {
	for _, tc := range []struct {
		name     string
		packages []string
		exit     string
	}{
		{"empty inventory", nil, "0"},
		{"go list failure after output", []string{shardModule, shardModule + "/cmd/mtix", shardModule + "/internal/store/sqlite"}, "7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHARD_TEST_EXIT", tc.exit)
			root := shardFixture(t, tc.packages, shardManifests())
			out, err := runShardGuard(root, "check")
			require.Error(t, err, out)
			require.Contains(t, out, "cannot enumerate")
		})
	}
}

func TestGoShards_ValidPartition_ListsSelectedPackages(t *testing.T) {
	manifests := shardManifests()
	packages := []string{shardModule, shardModule + "/cmd/mtix", shardModule + "/internal/store/sqlite", shardModule + "/node_modules/ignored"}
	root := shardFixture(t, packages, manifests)
	for _, shard := range []string{"sqlite", "cli", "other"} {
		t.Run(shard, func(t *testing.T) {
			out, err := runShardGuard(root, "list", shard)
			require.NoError(t, err, out)
			require.Equal(t, strings.Join(manifests[shard], "\n")+"\n", out)
		})
	}
	for _, args := range [][]string{nil, {"list", "unknown"}, {"list"}, {"check", "extra"}} {
		out, err := runShardGuard(root, args...)
		require.Error(t, err, out)
		require.Contains(t, out, "usage:")
	}
}
