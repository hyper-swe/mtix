// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-272.1: the hash split of the cmd/mtix tests left shards 2 and 3 at 16 to
// 19 minutes while shards 1 and 4 took 11 to 14. A checked-in pin file puts the
// known-heavy tests on chosen shards; every other test keeps its hash shard.
// The guard still proves each test is in exactly one shard, and refuses a pin
// that cannot be honoured, so a pin can never drop or duplicate a test.
package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const cmdPinFile = "cmd-mtix-shard-pins.txt"

func writePins(t *testing.T, root, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", cmdPinFile), []byte(body), 0o600))
}

func TestCmdMtixShards_PinnedTest_RunsOnlyInItsPinnedShard(t *testing.T) {
	_, text := fakeListing(80)
	root := cmdShardFixture(t, text, "0")
	for shard := 1; shard <= cmdShardCount; shard++ {
		writePins(t, root, "# heavy\nTestFeature7_Case_Works "+string(rune('0'+shard))+"\n")
		out, err := runCmdShards(root, "check")
		require.NoError(t, err, out)
		require.Equal(t, []int{shard}, shardOf(t, root, "TestFeature7_Case_Works"))
	}
}

func TestCmdMtixShards_PinWithoutTrailingNewline_IsHonoured(t *testing.T) {
	_, text := fakeListing(60)
	root := cmdShardFixture(t, text, "0")
	writePins(t, root, "TestFeature3_Case_Works 1\nTestFeature4_Case_Works 2")
	out, err := runCmdShards(root, "check")
	require.NoError(t, err, out)
	require.Equal(t, []int{1}, shardOf(t, root, "TestFeature3_Case_Works"))
	require.Equal(t, []int{2}, shardOf(t, root, "TestFeature4_Case_Works"))
}

func TestCmdMtixShards_PinsLeaveEveryOtherTestOnItsHashShard(t *testing.T) {
	names, text := fakeListing(80)
	root := cmdShardFixture(t, text, "0")
	before := map[string][]int{}
	for _, n := range names {
		before[n] = shardOf(t, root, n)
	}
	writePins(t, root, "TestFeature3_Case_Works 1\nTestFeature4_Case_Works 1\n")
	for _, n := range names {
		if n == "TestFeature3_Case_Works" || n == "TestFeature4_Case_Works" {
			require.Equal(t, []int{1}, shardOf(t, root, n))
			continue
		}
		require.Equal(t, before[n], shardOf(t, root, n), n)
	}
}

func TestCmdMtixShards_InvalidPins_Fail(t *testing.T) {
	_, text := fakeListing(60)
	cases := []struct{ name, pins, want string }{
		{"pin for a test that is not listed", "TestGone_Case_Works 2\n", "stale pin"},
		{"shard out of range", "TestFeature1_Case_Works 9\n", "invalid pin"},
		{"shard zero", "TestFeature1_Case_Works 0\n", "invalid pin"},
		{"missing shard", "TestFeature1_Case_Works\n", "invalid pin"},
		{"extra field", "TestFeature1_Case_Works 2 3\n", "invalid pin"},
		{"unsafe name", "TestFeature1.*  2\n", "invalid pin"},
		{"duplicate pin", "TestFeature1_Case_Works 2\nTestFeature1_Case_Works 3\n", "duplicate pin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := cmdShardFixture(t, text, "0")
			writePins(t, root, tc.pins)
			for _, args := range [][]string{{"check"}, {"pattern", "1"}} {
				out, err := runCmdShards(root, args...)
				require.Error(t, err, out)
				require.Contains(t, out, tc.want)
			}
		})
	}
}

// The real pin file must exist, name only plausible tests and spread the heavy
// tests over every shard, or the balance would silently regress.
func TestCmdMtixShards_RealPinFile_IsPresentAndSpansEveryShard(t *testing.T) {
	raw, err := os.ReadFile(cmdPinFile)
	require.NoError(t, err)
	perShard := map[string]int{}
	seen := map[string]bool{}
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(strings.SplitN(l, "#", 2)[0])
		if l == "" {
			continue
		}
		f := strings.Fields(l)
		require.Len(t, f, 2, l)
		require.Regexp(t, `^Test[A-Za-z0-9_]+$`, f[0])
		require.False(t, seen[f[0]], "duplicate pin %s", f[0])
		seen[f[0]] = true
		perShard[f[1]]++
	}
	for s := 1; s <= cmdShardCount; s++ {
		require.NotZero(t, perShard[string(rune('0'+s))], "no heavy test pinned to shard %d", s)
	}
}

// MTIX-272.1: the docs must say six shards and the current check counts.
func TestCmdMtixShards_ShardCountWording_IsConsistent(t *testing.T) {
	gates, err := os.ReadFile("../docs/CI-GATES.md")
	require.NoError(t, err)
	require.NotContains(t, string(gates), "three shards per OS (four since MTIX-272)")
	for _, stale := range []string{"Four `cmd/mtix` test shards", "| 8 (PR: 4) | Four `cmd/mtix`", "Five `cmd/mtix` test shards", "**29 CI checks**", "**39 CI checks**", "**32 CI checks**", "**43 CI checks**", "runs 10 macOS jobs", "runs 11 macOS jobs", "All 39 ordinary", "All 43 ordinary", "All 47 ordinary", "**35 CI checks**", "**47 CI checks**", "runs 12 macOS jobs", "twenty-six on push and nightly, fourteen on a PR"} {
		require.NotContains(t, string(gates), stale)
	}
	for _, want := range []string{"| 12 (PR: 6) | Six `cmd/mtix` test shards", "**37 CI checks**", "**51 CI checks**", "runs 15 macOS jobs", "thirty on push and nightly, sixteen on a PR", "All 51 ordinary"} {
		require.Contains(t, string(gates), want)
	}
	require.Regexp(t, regexp.MustCompile(`It now runs as .test-go-cmd.: six shards per\s+OS`), string(gates))
	contrib, err := os.ReadFile("../CONTRIBUTING.md")
	require.NoError(t, err)
	require.Contains(t, string(contrib), "all 37 of its checks")
	require.Contains(t, string(contrib), "all 51 (thirty Go race")
	quality, err := os.ReadFile("../QUALITY-STANDARDS.md")
	require.NoError(t, err)
	for _, stale := range []string{"four deterministic shards of `./cmd/mtix/`", "five deterministic shards of `./cmd/mtix/`"} {
		require.NotContains(t, string(quality), stale)
	}
	require.Contains(t, string(quality), "six deterministic shards of `./cmd/mtix/`")
	bar, err := os.ReadFile("remote-bar.sh")
	require.NoError(t, err)
	require.NotContains(t, string(bar), "three cmd/mtix shards")
	require.NotContains(t, string(bar), "five cmd/mtix shards")
	require.Contains(t, string(bar), "six cmd/mtix shards")
}
