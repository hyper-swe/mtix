// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// SQLite PR race shards keep the full compiled test inventory and the 20m
// timeout on both operating systems (MTIX-112.17.29).
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

func TestGoWorkflow_SQLiteShards_CoverBothOSesWithUnchangedFlags(t *testing.T) {
	jobs := readCIJobs(t)
	job, ok := jobs["test-go-sqlite"]
	require.True(t, ok, "SQLite needs its own test-shard matrix")
	require.Equal(t, "test-go-sqlite (${{ matrix.os }}, shard ${{ matrix.shard }})", job.Name)
	require.Equal(t, "${{ matrix.os }}", job.RunsOn)
	require.Equal(t, []string{"ubuntu-22.04", "macos-latest"}, job.Strategy.Matrix.OS)
	require.Equal(t, []string{"1", "2", "3"}, job.Strategy.Matrix.Shard)
	require.Empty(t, job.Strategy.Matrix.Include)
	require.Empty(t, job.Strategy.Matrix.Exclude)
	require.NotNil(t, job.Strategy.FailFast)
	require.False(t, *job.Strategy.FailFast)
	require.Empty(t, job.If)
	require.Nil(t, job.ContinueOnError)
	var packageGuard, testGuard, race, fullHistory bool
	for _, step := range job.Steps {
		require.Empty(t, step.If)
		require.Nil(t, step.ContinueOnError)
		packageGuard = packageGuard || step.Run == "bash scripts/ci-go-shards.sh check"
		testGuard = testGuard || step.Run == "bash scripts/ci-go-shards.sh test-check internal/store/sqlite 3"
		if strings.Contains(step.Run, "go test") {
			race = true
			require.Equal(t, "${{ matrix.shard }}", step.Env["SHARD"])
			require.Equal(t, "pattern=$(bash scripts/ci-go-shards.sh test-pattern internal/store/sqlite 3 \"$SHARD\")\ngo test ./internal/store/sqlite/ -race -count=1 -timeout 20m -run \"$pattern\"\n", step.Run)
		}
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			fullHistory = step.With["fetch-depth"] == 0
		}
	}
	require.True(t, packageGuard)
	require.True(t, testGuard)
	require.True(t, race)
	require.True(t, fullHistory)
}

func sqliteWorkflowCommands(t *testing.T) string {
	t.Helper()
	job, ok := readCIJobs(t)["test-go-sqlite"]
	require.True(t, ok)
	var commands []string
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "scripts/ci-go-shards.sh") {
			commands = append(commands, step.Run)
		}
	}
	require.Len(t, commands, 3)
	return strings.Join(commands, "\n")
}

func TestGoWorkflow_SQLiteCommands_SelectOneShardAndPropagateFailures(t *testing.T) {
	commands := sqliteWorkflowCommands(t)
	for _, failure := range []string{"", "package-check", "test-check", "pattern", "go"} {
		t.Run("failure="+failure, func(t *testing.T) {
			root := sqliteCommandFixture(t)
			cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", commands)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAIL_AT="+failure, "SHARD=2", "COMMAND_LOG="+filepath.Join(root, "go.log"))
			out, err := cmd.CombinedOutput()
			if failure != "" {
				require.Error(t, err, string(out))
				return
			}
			require.NoError(t, err, string(out))
			log, err := os.ReadFile(filepath.Join(root, "go.log"))
			require.NoError(t, err)
			require.Equal(t, "test ./internal/store/sqlite/ -race -count=1 -timeout 20m -run ^(TestShard2)$\n", string(log))
		})
	}
}

func sqliteCommandFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "scripts"), 0o750))
	require.NoError(t, os.Mkdir(filepath.Join(root, "bin"), 0o750))
	script := `#!/usr/bin/env bash
set -eu
case "$*" in
check) [ "$FAIL_AT" != package-check ] ;;
'test-check internal/store/sqlite 3') [ "$FAIL_AT" != test-check ] ;;
'test-pattern internal/store/sqlite 3 2')
  [ "$FAIL_AT" != pattern ]
  printf '%s\n' '^(TestShard2)$'
  ;;
*) exit 99 ;;
esac
`
	goShim := "#!/usr/bin/env bash\nset -eu\nprintf '%s\\n' \"$*\" >>\"$COMMAND_LOG\"\n[ \"$FAIL_AT\" != go ]\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "ci-go-shards.sh"), []byte(script), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin", "go"), []byte(goShim), 0o750)) //nolint:gosec // test-owned executable shim
	return root
}

func TestSQLiteShards_CompiledInventory_EachNameSelectedExactlyOnce(t *testing.T) {
	cmd := exec.Command("go", "test", "-list", ".*", "./internal/store/sqlite/")
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	var names []string
	unique := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Test") || strings.HasPrefix(line, "Example") || strings.HasPrefix(line, "Fuzz") {
			names = append(names, line)
			unique[line] = true
		}
	}
	require.NotEmpty(t, names)
	patterns := make([]*regexp.Regexp, 3)
	for i := range patterns {
		out, err := exec.Command("bash", "ci-go-shards.sh", "test-pattern", "internal/store/sqlite", "3", fmt.Sprint(i+1)).CombinedOutput()
		require.NoError(t, err, string(out))
		patterns[i], err = regexp.Compile(strings.TrimSpace(string(out)))
		require.NoError(t, err)
	}
	// Internal and external test packages can register the same name. Every
	// registration must match one shard; the script reports unique selectors.
	for _, name := range names {
		hits := 0
		for _, pattern := range patterns {
			if pattern.MatchString(name) {
				hits++
			}
			require.False(t, pattern.MatchString(name+"_NotListed"), "pattern must select whole names")
		}
		require.Equal(t, 1, hits, "%s must run in exactly one SQLite shard", name)
	}
	check, err := exec.Command("bash", "ci-go-shards.sh", "test-check", "internal/store/sqlite", "3").CombinedOutput()
	require.NoError(t, err, string(check))
	require.Contains(t, string(check), fmt.Sprintf("%d tests in 3 shards, split is exact", len(unique)))
}
