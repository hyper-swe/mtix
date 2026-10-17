// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.17.24: the cmd/mtix tests are split into deterministic shards for
// the PostgreSQL CI jobs and the remote bar. Every test must belong to exactly
// one shard, and a guard must fail when the split is not exact. These tests run
// the real script against a fake go that lists test names.
package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const cmdShardCount = 3

func cmdShardFixture(t *testing.T, listing string, exit string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o750))
	source, err := os.ReadFile("cmd-mtix-shards.sh")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "cmd-mtix-shards.sh"), source, 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "bin"), 0o750))
	shim := "#!/usr/bin/env bash\n[ \"$*\" = 'test -list .* ./cmd/mtix/' ] || exit 99\ncat \"$SHARD_TEST_LIST\"\nexit \"$SHARD_TEST_EXIT\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin", "go"), []byte(shim), 0o750)) //nolint:gosec // test stub must be executable
	require.NoError(t, os.WriteFile(filepath.Join(root, "listing.txt"), []byte(listing), 0o600))
	t.Setenv("SHARD_TEST_EXIT", exit)
	return root
}

func runCmdShards(root string, args ...string) (string, error) {
	cmd := exec.Command("bash", append([]string{filepath.Join(root, "scripts", "cmd-mtix-shards.sh")}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SHARD_TEST_LIST="+filepath.Join(root, "listing.txt"), "SHARD_TEST_EXIT="+os.Getenv("SHARD_TEST_EXIT"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func fakeListing(n int) (names []string, text string) {
	for i := 0; i < n; i++ {
		names = append(names, fmt.Sprintf("TestFeature%d_Case_Works", i))
	}
	names = append(names, "ExampleUsage", "FuzzParse")
	return names, strings.Join(names, "\n") + "\nok  \tgithub.com/hyper-swe/mtix/cmd/mtix\t0.012s\n"
}

// shardOf returns the shards whose run pattern selects name.
func shardOf(t *testing.T, root, name string) []int {
	t.Helper()
	var hit []int
	for i := 1; i <= cmdShardCount; i++ {
		pattern, err := runCmdShards(root, "pattern", fmt.Sprint(i))
		require.NoError(t, err, pattern)
		if regexp.MustCompile(strings.TrimSpace(pattern)).MatchString(name) {
			hit = append(hit, i)
		}
	}
	return hit
}

func TestCmdMtixShards_EveryTestInExactlyOneShard(t *testing.T) {
	names, text := fakeListing(300)
	root := cmdShardFixture(t, text, "0")
	out, err := runCmdShards(root, "check")
	require.NoError(t, err, out)
	sizes := make([]int, cmdShardCount+1)
	for _, n := range names {
		hit := shardOf(t, root, n)
		require.Len(t, hit, 1, "%s must be in exactly one shard, got %v", n, hit)
		sizes[hit[0]]++
	}
	for i := 1; i <= cmdShardCount; i++ {
		require.Greater(t, sizes[i], len(names)/(cmdShardCount*3), "shard %d is badly unbalanced: %v", i, sizes)
	}
}

func TestCmdMtixShards_Assignment_IsDeterministicAcrossRuns(t *testing.T) {
	_, text := fakeListing(50)
	root := cmdShardFixture(t, text, "0")
	first, err := runCmdShards(root, "pattern", "2")
	require.NoError(t, err, first)
	second, err := runCmdShards(root, "pattern", "2")
	require.NoError(t, err, second)
	require.Equal(t, first, second)
	// A new test lands in some shard with no manifest edit, and the shards
	// that did not gain it keep their pattern.
	_, bigger := fakeListing(51)
	require.NoError(t, os.WriteFile(filepath.Join(root, "listing.txt"), []byte(bigger), 0o600))
	for _, s := range []string{"1", "2", "3"} {
		out, err := runCmdShards(root, "pattern", s)
		require.NoError(t, err, out)
	}
	require.Len(t, shardOf(t, root, "TestFeature50_Case_Works"), 1)
}

func TestCmdMtixShards_InvalidInputs_Fail(t *testing.T) {
	_, text := fakeListing(30)
	cases := []struct {
		name, listing, exit string
		args                []string
		want                string
	}{
		{"list failure", text, "7", []string{"check"}, "cannot list"},
		{"empty listing", "ok  \tx\t0.1s\n", "0", []string{"check"}, "no tests"},
		{"too few tests to fill every shard", "TestA\nTestB\n", "0", []string{"check"}, "empty shard"},
		{"shard zero", text, "0", []string{"pattern", "0"}, "usage:"},
		{"shard out of range", text, "0", []string{"pattern", fmt.Sprint(cmdShardCount + 1)}, "usage:"},
		{"shard not a number", text, "0", []string{"pattern", "a"}, "usage:"},
		{"missing shard", text, "0", []string{"pattern"}, "usage:"},
		{"unknown mode", text, "0", []string{"run"}, "usage:"},
		{"check with extra argument", text, "0", []string{"check", "1"}, "usage:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := cmdShardFixture(t, tc.listing, tc.exit)
			out, err := runCmdShards(root, tc.args...)
			require.Error(t, err, out)
			require.Contains(t, out, tc.want)
		})
	}
}

func TestCmdMtixShards_RealPackage_PartitionsExactly(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles cmd/mtix tests; skipped with -short")
	}
	listCmd := exec.Command("go", "test", "-list", ".*", "./cmd/mtix/") //nolint:gosec // fixed arguments
	listCmd.Dir = ".."
	list, err := listCmd.Output()
	require.NoError(t, err)
	var names []string
	for _, l := range strings.Split(string(list), "\n") {
		if regexp.MustCompile(`^(Test|Example|Fuzz)\w*$`).MatchString(l) {
			names = append(names, l)
		}
	}
	require.NotEmpty(t, names)
	script, err := filepath.Abs("cmd-mtix-shards.sh")
	require.NoError(t, err)
	patterns := make([]*regexp.Regexp, cmdShardCount+1)
	for i := 1; i <= cmdShardCount; i++ {
		out, err := exec.Command("bash", script, "pattern", fmt.Sprint(i)).CombinedOutput() //nolint:gosec // test-owned script
		require.NoError(t, err, string(out))
		patterns[i] = regexp.MustCompile(strings.TrimSpace(string(out)))
	}
	for _, n := range names {
		hits := 0
		for i := 1; i <= cmdShardCount; i++ {
			if patterns[i].MatchString(n) {
				hits++
			}
		}
		require.Equal(t, 1, hits, "%s", n)
	}
}

// make test-race-core plus the cmd/mtix shards must equal make test-race.
func TestMakefile_TestRaceCore_IsTestRaceWithoutCmdMtix(t *testing.T) {
	pkgs := func(target string) []string {
		cmd := exec.Command("make", "-n", target)
		cmd.Dir = ".."
		out, err := cmd.Output()
		require.NoError(t, err)
		var res []string
		for _, f := range strings.Fields(string(out)) {
			if strings.HasPrefix(f, shardModule) {
				res = append(res, f)
			}
		}
		return res
	}
	full, core := pkgs("test-race"), pkgs("test-race-core")
	require.NotEmpty(t, core)
	require.Contains(t, full, shardModule+"/cmd/mtix")
	require.NotContains(t, core, shardModule+"/cmd/mtix")
	var withoutCmd []string
	for _, p := range full {
		if p != shardModule+"/cmd/mtix" {
			withoutCmd = append(withoutCmd, p)
		}
	}
	require.Equal(t, withoutCmd, core)
}
