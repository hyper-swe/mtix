// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.51: the inventory and the selected test build must both enable race.
package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type raceInventoryCase struct {
	name, dir string
	shards    int
}

func raceInventoryCases() []raceInventoryCase {
	return []raceInventoryCase{
		{"service", "internal/service", 2},
		{"sqlite", "internal/store/sqlite", 3},
		{"cmd", "cmd/mtix", 3},
	}
}

func (c raceInventoryCase) checkArgs() []string {
	if c.name == "cmd" {
		return []string{"bash", "scripts/cmd-mtix-shards.sh", "check"}
	}
	return []string{"bash", "scripts/ci-go-shards.sh", "test-check", c.dir, strconv.Itoa(c.shards)}
}

func (c raceInventoryCase) patternArgs(shard int) []string {
	if c.name == "cmd" {
		return []string{"bash", "scripts/cmd-mtix-shards.sh", "pattern", strconv.Itoa(shard)}
	}
	return []string{"bash", "scripts/ci-go-shards.sh", "test-pattern", c.dir, strconv.Itoa(c.shards), strconv.Itoa(shard)}
}

func raceInventoryCommand(t *testing.T, root string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), args[0], args[1:]...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	return cmd.CombinedOutput()
}

func raceInventoryFixture(t *testing.T, c raceInventoryCase) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, c.dir), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "scripts"), 0o700))
	for _, name := range []string{"ci-go-shards.sh", "cmd-mtix-shards.sh"} {
		raw, err := os.ReadFile(name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", name), raw, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/shardfixture\n\ngo 1.26.0\n"), 0o600))
	var base strings.Builder
	_, err := base.WriteString("package fixture\nimport \"testing\"\n")
	require.NoError(t, err)
	for i := range 9 {
		_, err = fmt.Fprintf(&base, "func TestBase%d(t *testing.T) {}\n", i)
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, c.dir, "base_test.go"), []byte(base.String()), 0o600))
	race := "//go:build race\n\npackage fixture\nimport \"testing\"\nfunc TestRaceOnly(t *testing.T) { t.Fatal(\"race-only sentinel\") }\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, c.dir, "race_only_test.go"), []byte(race), 0o600))
	return root
}

func requireRaceOnlyListed(t *testing.T, root string, c raceInventoryCase) {
	t.Helper()
	out, err := raceInventoryCommand(t, root, "go", "test", "-race", "-list", "^TestRaceOnly$", "./"+c.dir+"/")
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), "TestRaceOnly")
}

func TestRaceShards_RaceTaggedTest_RunsInExactlyOneShard(t *testing.T) {
	for _, c := range raceInventoryCases() {
		t.Run(c.name, func(t *testing.T) {
			root := raceInventoryFixture(t, c)
			requireRaceOnlyListed(t, root, c)
			check, err := raceInventoryCommand(t, root, c.checkArgs()...)
			require.NoError(t, err, string(check))
			var selected string
			hits := 0
			for shard := 1; shard <= c.shards; shard++ {
				out, err := raceInventoryCommand(t, root, c.patternArgs(shard)...)
				require.NoError(t, err, string(out))
				pattern := strings.TrimSpace(string(out))
				re, err := regexp.Compile(pattern)
				require.NoError(t, err)
				if re.MatchString("TestRaceOnly") {
					hits++
					selected = pattern
				}
			}
			require.Equal(t, 1, hits, "race build contains the test, but inventory says %s", check)
			out, err := raceInventoryCommand(t, root, "go", "test", "-race", "-count=1", "-run", selected, "./"+c.dir+"/")
			require.Error(t, err, "the selected race-only test intentionally fails")
			require.Contains(t, string(out), "race-only sentinel")
		})
	}
}

func TestRaceShards_OmittedRaceTaggedTest_StopsTheJob(t *testing.T) {
	for _, c := range raceInventoryCases() {
		t.Run(c.name, func(t *testing.T) {
			root := raceInventoryFixture(t, c)
			requireRaceOnlyListed(t, root, c)
			name := "ci-go-shards.sh"
			if c.name == "cmd" {
				name = "cmd-mtix-shards.sh"
			}
			path := filepath.Join(root, "scripts", name)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			old := "grep -E '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' \"$scratch/listing\" | sort -u >\"$scratch/names\""
			require.Contains(t, string(raw), old)
			changed := strings.Replace(string(raw), old, strings.Replace(old, "| sort -u", "| grep -v '^TestRaceOnly$' | sort -u", 1), 1)
			require.NoError(t, os.WriteFile(path, []byte(changed), 0o600))
			commands := strings.Join(c.checkArgs(), " ") + "\nprintf reached > after-check\n"
			out, err := raceInventoryCommand(t, root, "bash", "-e", "-o", "pipefail", "-c", commands)
			require.Error(t, err, string(out))
			require.Contains(t, string(out), "selected by no shard")
			require.Contains(t, string(out), "TestRaceOnly")
			require.NoFileExists(t, filepath.Join(root, "after-check"))
		})
	}
}

func TestRaceShards_EmptyPattern_FailsInsteadOfRunningEverything(t *testing.T) {
	for _, c := range raceInventoryCases() {
		t.Run(c.name, func(t *testing.T) {
			root := raceInventoryFixture(t, c)
			require.NoError(t, os.Remove(filepath.Join(root, c.dir, "race_only_test.go")))
			source := "package fixture\nimport \"testing\"\nfunc TestOnly(t *testing.T) {}\n"
			require.NoError(t, os.WriteFile(filepath.Join(root, c.dir, "base_test.go"), []byte(source), 0o600))
			nonempty := 0
			for shard := 1; shard <= c.shards; shard++ {
				out, err := raceInventoryCommand(t, root, c.patternArgs(shard)...)
				if err != nil {
					require.Contains(t, string(out), "empty shard")
					continue
				}
				nonempty++
				require.NotEmpty(t, strings.TrimSpace(string(out)))
			}
			require.Equal(t, 1, nonempty)
		})
	}
}
