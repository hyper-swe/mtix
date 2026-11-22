// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-131.2: internal/store/sqlite runs in six test-name shards everywhere
// (ordinary CI on both OSes and both remote-bar matrices), and its known-heavy
// tests are pinned to a shard from a checked-in list so the hash split cannot
// stack them. These tests prove a pin is honoured, an unpinned test keeps its
// hash shard, and every broken pin file fails instead of dropping or repeating
// a test.
package scripts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sqliteShardCount is the one SQLite shard count of ci.yml, remote-bar.yml
// and scripts/ci-go-shard-pins/internal-store-sqlite.txt.
const sqliteShardCount = 6

const sqlitePinFile = "ci-go-shard-pins/internal-store-sqlite.txt"

// writeSlowPins writes the pin file of the fixture package internal/slow.
func writeSlowPins(t *testing.T, root, text string) {
	t.Helper()
	dir := filepath.Join(root, "scripts", "ci-go-shard-pins")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "internal-slow.txt"), []byte(text), 0o600))
}

// testShardOf returns the shard whose test-pattern selects name.
func testShardOf(t *testing.T, root string, shards int, name string) int {
	t.Helper()
	found := 0
	for k := 1; k <= shards; k++ {
		pat, err := runTestShards(root, "test-pattern", "internal/slow", strconv.Itoa(shards), strconv.Itoa(k))
		require.NoError(t, err, pat)
		if regexp.MustCompile(strings.TrimSpace(pat)).MatchString(name) {
			require.Zero(t, found, "%s selected by shards %d and %d", name, found, k)
			found = k
		}
	}
	require.NotZero(t, found, "%s selected by no shard", name)
	return found
}

func TestTestShards_PinFile_PinnedTestsMoveAndTheRestKeepTheirHashShard(t *testing.T) {
	names, listing := fakeTestListing(40)
	plain := testShardFixture(t, listing, "0")
	pinned := testShardFixture(t, listing, "0")
	hashShard := map[string]int{}
	for _, n := range names {
		hashShard[n] = testShardOf(t, plain, 3, n)
	}
	// Pin two tests to a shard other than their hash shard.
	moved := map[string]int{}
	for _, n := range names[:2] {
		moved[n] = hashShard[n]%3 + 1
	}
	writeSlowPins(t, pinned, "# measured seconds\nshards 3\n"+
		names[0]+" "+strconv.Itoa(moved[names[0]])+"  # 99s\n\n"+
		names[1]+" "+strconv.Itoa(moved[names[1]])+"\n")
	out, err := runTestShards(pinned, "test-check", "internal/slow", "3")
	require.NoError(t, err, out)
	require.Contains(t, out, "40 tests in 3 shards, split is exact")
	for _, n := range names {
		want := hashShard[n]
		if s, ok := moved[n]; ok {
			want = s
		}
		require.Equal(t, want, testShardOf(t, pinned, 3, n), n)
	}
}

func TestTestShards_BrokenPinFile_Fails(t *testing.T) {
	names, listing := fakeTestListing(12)
	for _, tc := range []struct{ name, pins, want string }{
		{"no shard count", names[0] + " 1\n", "first line must be"},
		{"shard count differs from the caller", "shards 4\n" + names[0] + " 1\n", "pins are for 4 shards"},
		{"shard count twice", "shards 3\nshards 3\n", "invalid pin"},
		{"hash range above the count", "shards 3 hash 4\n" + names[0] + " 1\n", "hash 4 exceeds 3 shards"},
		{"hash range zero", "shards 3 hash 0\n" + names[0] + " 1\n", "first line must be"},
		{"hash keyword misspelt", "shards 3 hsah 2\n" + names[0] + " 1\n", "first line must be"},
		{"pins-only shard left empty", "shards 3 hash 2\n" + names[0] + " 1\n", "empty shard 3"},
		{"shard zero", "shards 3\n" + names[0] + " 0\n", "invalid pin"},
		{"shard above the count", "shards 3\n" + names[0] + " 4\n", "invalid pin"},
		{"empty file", "", "first line must be"},
		{"comments and blank lines only", "# measured later\n\n   # nothing yet\n", "first line must be"},
		{"missing shard", "shards 3\n" + names[0] + "\n", "invalid pin"},
		{"extra field", "shards 3\n" + names[0] + " 1 2\n", "invalid pin"},
		{"not a test name", "shards 3\nhelper 1\n", "invalid pin"},
		{"duplicate pin", "shards 3\n" + names[0] + " 1\n" + names[0] + " 2\n", "duplicate pin " + names[0]},
		{"stale pin", "shards 3\nTestGone_Renamed 1\n", "stale pin TestGone_Renamed"},
		{"pin empties a shard", "shards 2\n" + strings.Join(pinAll(names, 1), "\n") + "\n", "empty shard 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testShardFixture(t, listing, "0")
			writeSlowPins(t, root, tc.pins)
			shards := "3"
			if strings.HasPrefix(tc.pins, "shards 2") {
				shards = "2"
			}
			for _, args := range [][]string{
				{"test-check", "internal/slow", shards},
				{"test-pattern", "internal/slow", shards, "1"},
			} {
				out, err := runTestShards(root, args...)
				if (tc.name == "pin empties a shard" || tc.name == "pins-only shard left empty") && args[0] == "test-pattern" {
					require.NoError(t, err, out, "shard 1 itself is not empty")
					continue
				}
				require.Error(t, err, out)
				require.Contains(t, out, tc.want, args)
				require.NotContains(t, out, "split is exact")
			}
		})
	}
}

func pinAll(names []string, shard int) []string {
	lines := make([]string, 0, len(names))
	for _, n := range names {
		lines = append(lines, n+" "+strconv.Itoa(shard))
	}
	return lines
}

// "shards 3 hash 2": unpinned tests hash over shards 1..2 only, so shard 3
// holds exactly its pinned tests (a slow test that should run alone).
func TestTestShards_HashRange_ShardsAboveItHoldOnlyTheirPins(t *testing.T) {
	names, listing := fakeTestListing(30)
	root := testShardFixture(t, listing, "0")
	writeSlowPins(t, root, "shards 3 hash 2\n"+names[5]+" 3\n")
	out, err := runTestShards(root, "test-check", "internal/slow", "3")
	require.NoError(t, err, out)
	require.Contains(t, out, "30 tests in 3 shards, split is exact")
	pat, err := runTestShards(root, "test-pattern", "internal/slow", "3", "3")
	require.NoError(t, err, pat)
	require.Equal(t, "^("+names[5]+")$\n", pat)
	plain := testShardFixture(t, listing, "0")
	for i, n := range names {
		if i == 5 {
			continue
		}
		require.Equal(t, testShardOf(t, plain, 2, n), testShardOf(t, root, 3, n), "%s keeps its two-way hash shard", n)
	}
}

// A package without a pin file keeps the plain hash split for any count, so
// internal/service and older trees need no pin file.
func TestTestShards_NoPinFile_AnyCountStaysAHashSplit(t *testing.T) {
	names, listing := fakeTestListing(30)
	root := testShardFixture(t, listing, "0")
	for _, shards := range []int{2, 4, 6} {
		out, err := runTestShards(root, "test-check", "internal/slow", strconv.Itoa(shards))
		require.NoError(t, err, out)
		require.Contains(t, out, "30 tests in "+strconv.Itoa(shards)+" shards, split is exact")
	}
	require.NotZero(t, testShardOf(t, root, 6, names[0]))
}

// readSQLitePins parses the real pin file: its shard count and the pins.
func readSQLitePins(t *testing.T) (int, map[string]int) {
	t.Helper()
	count, _, pins := readSQLitePinFile(t)
	return count, pins
}

// readSQLitePinFile parses the real pin file: its shard count, its hash
// range (the count when the header has no hash) and the pins.
func readSQLitePinFile(t *testing.T) (int, int, map[string]int) {
	t.Helper()
	raw, err := os.ReadFile(sqlitePinFile)
	require.NoError(t, err)
	count, hash, pins, err := parseSQLitePins(string(raw))
	require.NoError(t, err)
	return count, hash, pins
}

// parseSQLitePins parses pin-file text. A line with fewer than two fields is
// an error naming the line, never an index panic.
func parseSQLitePins(text string) (int, int, map[string]int, error) {
	count, hash := 0, 0
	pins := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 2 {
			return 0, 0, nil, fmt.Errorf("pin line needs at least two fields: %q", line)
		}
		n, err := strconv.Atoi(f[1])
		if err != nil {
			return 0, 0, nil, fmt.Errorf("pin line %q: %w", line, err)
		}
		if f[0] == "shards" {
			if count != 0 {
				return 0, 0, nil, fmt.Errorf("second shard count line: %q", line)
			}
			count, hash = n, n
			switch {
			case len(f) == 4 && f[2] == "hash":
				if hash, err = strconv.Atoi(f[3]); err != nil {
					return 0, 0, nil, fmt.Errorf("pin line %q: %w", line, err)
				}
			case len(f) != 2:
				return 0, 0, nil, fmt.Errorf("bad shard count line: %q", line)
			}
			continue
		}
		if len(f) != 2 {
			return 0, 0, nil, fmt.Errorf("pin line needs exactly two fields: %q", line)
		}
		pins[f[0]] = n
	}
	return count, hash, pins, nil
}

// A one-word line in the pin file fails with the line, it does not panic.
func TestParseSQLitePins_OneWordLine_ReturnsErrorNamingTheLine(t *testing.T) {
	for _, text := range []string{"shards\n", "shards 6\nTestLonely\n", "TestLonely\n"} {
		require.NotPanics(t, func() {
			_, _, _, err := parseSQLitePins(text)
			require.Error(t, err, text)
			require.Contains(t, err.Error(), "two fields", text)
		}, text)
	}
}

// sqliteWriterReadsTest is the slowest SQLite test (about 860 s of the
// package's 4850 s under -race on three cores, MTIX-131.2): it runs alone on
// the pins-only last shard.
const sqliteWriterReadsTest = "TestWriterReads_CancelledMidQuery_LeaveNoStaleSnapshot"

// The real SQLite pins use the one shard count, place a pin on every shard,
// and pin every read-cancel sweep test (MTIX-264): those are the heaviest
// tests of the package, so a rename that drops one from the list fails here
// rather than silently stacking it by hash. The writer-read sweep is alone on
// the last shard, which the hash range leaves to pins.
func TestSQLiteShardPins_RealFile_UsesTheCountAndPinsTheReadCancelSweeps(t *testing.T) {
	count, hash, pins := readSQLitePinFile(t)
	require.Equal(t, sqliteShardCount, count)
	require.Equal(t, sqliteShardCount-1, hash, "the last shard holds pins only")
	require.Equal(t, sqliteShardCount, pins[sqliteWriterReadsTest])
	for name, s := range pins {
		if name != sqliteWriterReadsTest {
			require.NotEqual(t, sqliteShardCount, s, "%s shares the writer-read shard", name)
		}
	}
	perShard := map[int]int{}
	for name, s := range pins {
		require.True(t, s >= 1 && s <= count, "%s pinned to %d", name, s)
		perShard[s]++
	}
	require.Len(t, perShard, count, "every shard carries at least one pinned test")
	for _, name := range readCancelTestNames(t) {
		require.Contains(t, pins, name, "read-cancel sweep tests are pinned by measured cost")
	}
}

// One SQLite shard count everywhere: the ordinary CI matrix and its guard
// commands, the Linux and macOS remote-bar matrices, and the pin file.
func TestSQLiteShards_OneCountInCIRemoteBarAndPins(t *testing.T) {
	want := make([]string, sqliteShardCount)
	for i := range want {
		want[i] = strconv.Itoa(i + 1)
	}
	job := readCIJobs(t)["test-go-sqlite"]
	require.Equal(t, want, job.Strategy.Matrix.Shard)
	n := strconv.Itoa(sqliteShardCount)
	var check, pattern bool
	for _, step := range job.Steps {
		check = check || step.Run == "bash scripts/ci-go-shards.sh test-check internal/store/sqlite "+n
		pattern = pattern || strings.Contains(step.Run, "test-pattern internal/store/sqlite "+n+` "$SHARD"`)
	}
	require.True(t, check, "test-check uses %s shards", n)
	require.True(t, pattern, "test-pattern uses %s shards", n)
	jobs := readRBJobs(t)
	for _, name := range []string{linuxShardJob, macShardJob} {
		var got []string
		for _, inc := range jobs[name].Strategy.Matrix.Include {
			if inc["pkg"] != "internal/store/sqlite" {
				continue
			}
			require.Equal(t, n, inc["shards"], name)
			got = append(got, inc["shard"])
		}
		require.Equal(t, want, got, name)
	}
	count, _ := readSQLitePins(t)
	require.Equal(t, sqliteShardCount, count)
}
