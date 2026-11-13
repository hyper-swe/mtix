// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.17.42.6: the macOS remote bar shards internal/store/sqlite,
// internal/service and cmd/mtix into matrix jobs that mirror the Linux
// bar-shard and bar-cmd jobs, because the whole-package runs overran the 20m
// per-package timeout under -race on macOS. These PERMANENT tests parse the real
// remote-bar.yml and prove the macOS legs cover exactly what the Linux legs
// cover, with the same flags, so a dropped shard or a changed flag fails here.
package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	macBarJob      = "bar-macos"
	macShardJob    = "bar-macos-shard"
	macCmdJob      = "bar-macos-cmd"
	linuxShardJob  = "bar-shard"
	linuxCmdJob    = "bar-cmd"
	barModulePath  = "github.com/hyper-swe/mtix/"
	cmdMtixPackage = "cmd/mtix"
)

type rbStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

type rbJob struct {
	If       string            `yaml:"if"`
	RunsOn   string            `yaml:"runs-on"`
	Env      map[string]string `yaml:"env"`
	Strategy struct {
		Matrix struct {
			Include []map[string]string `yaml:"include"`
			Shard   []string            `yaml:"shard"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []rbStep `yaml:"steps"`
}

func readRBJobs(t *testing.T) map[string]rbJob {
	t.Helper()
	var wf struct {
		Jobs map[string]rbJob `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(readWorkflow(t)), &wf))
	return wf.Jobs
}

func (j rbJob) stepIndex(name string) int {
	for i, s := range j.Steps {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// goTestLines returns every logical go test command line of the job.
func (j rbJob) goTestLines() []string {
	var out []string
	for _, s := range j.Steps {
		for _, l := range strings.Split(s.Run, "\n") {
			if strings.Contains(l, "go test") {
				out = append(out, strings.TrimSpace(l))
			}
		}
	}
	return out
}

func (j rbJob) allRun() string {
	var b strings.Builder
	for _, s := range j.Steps {
		b.WriteString(s.Run + "\n")
	}
	return b.String()
}

// includeSet renders a matrix include list as a sorted, comparable slice.
func includeSet(inc []map[string]string) []string {
	var out []string
	for _, m := range inc {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, k+"="+m[k])
		}
		out = append(out, strings.Join(parts, ","))
	}
	sort.Strings(out)
	return out
}

func manifestPackages(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("remote-bar-packages/" + name + ".txt")
	require.NoError(t, err)
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func requireJobsPresent(t *testing.T, jobs map[string]rbJob) (mac, shard, cmd rbJob) {
	t.Helper()
	for _, name := range []string{macBarJob, macShardJob, macCmdJob} {
		j, ok := jobs[name]
		require.True(t, ok, "job %s missing", name)
		require.Equal(t, "${{ inputs.os == 'macos-latest' }}", j.If, name)
		require.Equal(t, "macos-latest", j.RunsOn, name)
	}
	return jobs[macBarJob], jobs[macShardJob], jobs[macCmdJob]
}

// (a) The macOS shard matrix equals the Linux bar-shard matrix.
func TestRemoteBarMacOS_ShardMatrix_EqualsLinuxBarShardSet(t *testing.T) {
	jobs := readRBJobs(t)
	_, shard, _ := requireJobsPresent(t, jobs)
	linux := jobs[linuxShardJob].Strategy.Matrix.Include
	require.NotEmpty(t, linux)
	require.Equal(t, includeSet(linux), includeSet(shard.Strategy.Matrix.Include),
		"macOS shards must be exactly the Linux bar-shard (pkg, shards, shard) set")
	// Every sharded package runs every shard 1..N, and the packages equal the manifest.
	counts := map[string]int{}
	seen := map[string]map[int]bool{}
	for _, m := range shard.Strategy.Matrix.Include {
		n, err := strconv.Atoi(m["shards"])
		require.NoError(t, err)
		k, err := strconv.Atoi(m["shard"])
		require.NoError(t, err)
		require.True(t, k >= 1 && k <= n, "shard %d outside 1..%d", k, n)
		if seen[m["pkg"]] == nil {
			seen[m["pkg"]] = map[int]bool{}
		}
		if prev, ok := counts[m["pkg"]]; ok {
			require.Equal(t, prev, n, "package %s has inconsistent shard counts", m["pkg"])
		}
		counts[m["pkg"]] = n
		seen[m["pkg"]][k] = true
	}
	var pkgs []string
	for pkg, n := range counts {
		require.Len(t, seen[pkg], n, "package %s must run every shard 1..%d", pkg, n)
		pkgs = append(pkgs, barModulePath+pkg)
	}
	sort.Strings(pkgs)
	want := manifestPackages(t, "sharded")
	sort.Strings(want)
	require.Equal(t, want, pkgs, "macOS sharded packages must equal scripts/remote-bar-packages/sharded.txt")
}

// (b) The macOS cmd shard set equals the Linux bar-cmd set and 1..SHARDS.
func TestRemoteBarMacOS_CmdShards_EqualLinuxSetAndScriptShardCount(t *testing.T) {
	jobs := readRBJobs(t)
	_, _, cmd := requireJobsPresent(t, jobs)
	linux := append([]string(nil), jobs[linuxCmdJob].Strategy.Matrix.Shard...)
	got := append([]string(nil), cmd.Strategy.Matrix.Shard...)
	sort.Strings(linux)
	sort.Strings(got)
	require.Equal(t, linux, got, "macOS cmd shards must equal the Linux bar-cmd shard list")
	src, err := os.ReadFile("cmd-mtix-shards.sh")
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^SHARDS=([0-9]+)$`).FindStringSubmatch(string(src))
	require.NotNil(t, m, "SHARDS not found in cmd-mtix-shards.sh")
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	var want []string
	for i := 1; i <= n; i++ {
		want = append(want, strconv.Itoa(i))
	}
	require.Equal(t, want, got, "macOS cmd shards must be exactly 1..SHARDS of cmd-mtix-shards.sh")
}

// (c) The macOS non-shard job takes packages only from the serial and parallel
// manifests; with the shards and cmd/mtix every package of go list is covered once.
func TestRemoteBarMacOS_NonShardJob_UsesManifestsAndEveryPackageIsCovered(t *testing.T) {
	jobs := readRBJobs(t)
	mac, _, _ := requireJobsPresent(t, jobs)
	run := stepRunByName(t, mac, "Run committed bar")
	require.Contains(t, run, "bar-list serial")
	require.Contains(t, run, "bar-list parallel")
	require.NotContains(t, run, "bar-list sharded", "sharded packages run only in the shard jobs")
	require.NotContains(t, run, barModulePath, "no literal package paths in the non-shard job")
	require.NotContains(t, run, "./...")
	require.True(t, mac.stepIndex("Verify exact package sets") >= 0, "bar-check step missing")
	require.Contains(t, stepRunByName(t, mac, "Verify exact package sets"), "ci-go-shards.sh bar-check")

	cover := map[string]int{barModulePath + cmdMtixPackage: 1}
	for _, name := range []string{"serial", "parallel", "sharded"} {
		for _, p := range manifestPackages(t, name) {
			cover[p]++
		}
	}
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = ".."
	out, err := cmd.Output()
	require.NoError(t, err)
	for _, p := range strings.Fields(string(out)) {
		if strings.Contains(p, "node_modules") {
			continue
		}
		require.Equal(t, 1, cover[p], "package %s must be owned by exactly one macOS bar leg", p)
		delete(cover, p)
	}
	require.Empty(t, cover, "manifests list packages that go list does not")
}

func stepRunByName(t *testing.T, j rbJob, name string) string {
	t.Helper()
	i := j.stepIndex(name)
	require.GreaterOrEqual(t, i, 0, "step %q missing", name)
	return j.Steps[i].Run
}

// (d) The flags are the Linux flags: -race -count=1 -timeout 20m, nothing skipped.
func TestRemoteBarMacOS_Flags_MatchLinuxAndSkipNothing(t *testing.T) {
	jobs := readRBJobs(t)
	mac, shard, cmd := requireJobsPresent(t, jobs)
	for name, j := range map[string]rbJob{macBarJob: mac, macShardJob: shard, macCmdJob: cmd} {
		lines := j.goTestLines()
		require.NotEmpty(t, lines, name)
		for _, l := range lines {
			require.Contains(t, l, "-race", name)
			require.Contains(t, l, "-count=1", name)
			require.Contains(t, l, "-timeout 20m", name)
		}
		for _, bad := range []string{"-skip", "-short"} {
			require.NotContains(t, j.allRun(), bad, "%s must not use %s", name, bad)
		}
	}
	require.Equal(t, jobs[linuxShardJob].goTestLines(), shard.goTestLines(), "macOS shard run line equals Linux")
	require.Equal(t, jobs[linuxCmdJob].goTestLines(), cmd.goTestLines(), "macOS cmd run line equals Linux")
}

// (e) Both coverage check steps precede the run steps.
func TestRemoteBarMacOS_CheckSteps_PrecedeRunSteps(t *testing.T) {
	jobs := readRBJobs(t)
	mac, shard, cmd := requireJobsPresent(t, jobs)
	for _, c := range []struct {
		j      rbJob
		checks []string
		run    string
	}{
		{mac, []string{"Verify snapshot tree", "Verify exact package sets"}, "Run committed bar"},
		{shard, []string{"Verify snapshot tree", "Verify exact package sets", "Verify exact package test coverage"}, "Run package shard"},
		{cmd, []string{"Verify snapshot tree", "Verify exact cmd/mtix test coverage"}, "Run cmd/mtix shard"},
	} {
		run := c.j.stepIndex(c.run)
		require.GreaterOrEqual(t, run, 0, c.run)
		for _, ch := range c.checks {
			i := c.j.stepIndex(ch)
			require.GreaterOrEqual(t, i, 0, ch)
			require.Less(t, i, run, "%s must precede %s", ch, c.run)
		}
	}
	require.Contains(t, stepRunByName(t, shard, "Verify exact package test coverage"), `ci-go-shards.sh test-check "$PKG" "$SHARDS"`)
	require.Contains(t, stepRunByName(t, cmd, "Verify exact cmd/mtix test coverage"), "cmd-mtix-shards.sh check")
}

// (f) bar-macos no longer runs make test-race but still lints and scans.
func TestRemoteBarMacOS_BarJob_NoTestRaceButLintAndGovulncheck(t *testing.T) {
	mac, _, _ := requireJobsPresent(t, readRBJobs(t))
	require.NotContains(t, mac.allRun(), "make test-race")
	require.Contains(t, stepRunByName(t, mac, "Run committed bar"), "make lint-go")
	require.Contains(t, stepRunByName(t, mac, "Run govulncheck"), "govulncheck")
}

// runCommittedBar runs the real "Run committed bar" step of bar-macos under
// bash -e from the repository root, with shims first on PATH: make does
// nothing, go records every `go test` command line (other go commands reach the
// real go, which bar-list needs), and bash fails any bar-list call whose
// manifest name is in failList. It returns the step's output, its exit code and
// the recorded go test command lines.
func runCommittedBar(t *testing.T, failList string) (string, int, []string) {
	t.Helper()
	realGo, err := exec.LookPath("go")
	require.NoError(t, err)
	realBash, err := exec.LookPath("bash")
	require.NoError(t, err)
	mac, _, _ := requireJobsPresent(t, readRBJobs(t))
	script := stepRunByName(t, mac, "Run committed bar")

	dir := t.TempDir()
	record := filepath.Join(dir, "gotest.log")
	shims := map[string]string{
		"make": "#!/bin/sh\nexit 0\n",
		"go":   "#!/bin/sh\nif [ \"$1\" = test ]; then echo \"$*\" >>\"" + record + "\"; exit 0; fi\nexec " + realGo + " \"$@\"\n",
		"bash": "#!/bin/sh\nif [ \"$2\" = bar-list ] && [ \"$3\" = \"" + failList + "\" ]; then echo 'shim: bar-list failed' >&2; exit 7; fi\nexec " + realBash + " \"$@\"\n",
	}
	for name, body := range shims {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700)) //nolint:gosec // G306: test shim must be executable
	}
	cmd := exec.Command(realBash, "-e", "-c", script)
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, runErr := cmd.CombinedOutput()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code = exitErr.ExitCode()
	} else {
		require.NoError(t, runErr, string(out))
	}
	logged, readErr := os.ReadFile(record)
	if readErr != nil {
		require.True(t, os.IsNotExist(readErr), readErr)
	}
	return string(out), code, strings.Split(strings.TrimSpace(string(logged)), "\n")
}

// (c2) The packages the bar-macos step really passes to go test are exactly
// the serial and parallel manifests, in that order, before the flags.
func TestRemoteBarMacOS_NonShardJob_PassesExactlySerialThenParallelPackages(t *testing.T) {
	out, code, lines := runCommittedBar(t, "")
	require.Equal(t, 0, code, out)
	require.Len(t, lines, 1, "exactly one go test run, got %v", lines)
	fields := strings.Fields(lines[0])
	require.Equal(t, "test", fields[0])
	var pkgs []string
	i := 1
	for ; i < len(fields) && !strings.HasPrefix(fields[i], "-"); i++ {
		pkgs = append(pkgs, fields[i])
	}
	want := append(manifestPackages(t, "serial"), manifestPackages(t, "parallel")...)
	require.Equal(t, want, pkgs, "go test must receive the serial then parallel manifests exactly")
	require.Equal(t, "-race -count=1 -p 2 -timeout 20m", strings.Join(fields[i:], " "))
}

// (c3) A failing bar-list must fail the step; a substitution that runs two
// lists would otherwise mask the first one's failure.
func TestRemoteBarMacOS_NonShardJob_FailingBarListFailsTheStepBeforeGoTest(t *testing.T) {
	for _, list := range []string{"serial", "parallel"} {
		t.Run(list, func(t *testing.T) {
			out, code, lines := runCommittedBar(t, list)
			require.NotEqual(t, 0, code, "a failing bar-list %s must fail the step: %s", list, out)
			require.Contains(t, out, "shim: bar-list failed")
			require.Equal(t, []string{""}, lines, "go test must not run after a bar-list failure")
		})
	}
}

// (g) Every new job checks out the scratch ref and carries the snapshot tree
// and base verification of the bar job, with the env it reads.
func TestRemoteBarMacOS_NewJobs_PinCheckoutRefAndSnapshotVerification(t *testing.T) {
	jobs := readRBJobs(t)
	mac, shard, cmd := requireJobsPresent(t, jobs)
	ref := stepRunByName(t, jobs["bar"], "Verify snapshot tree")
	require.Contains(t, ref, "BASE_COMMIT", "reference verify step lost its base check")
	for name, j := range map[string]rbJob{macShardJob: shard, macCmdJob: cmd, macBarJob: mac} {
		require.Equal(t, ref, stepRunByName(t, j, "Verify snapshot tree"), "%s: verify step must equal the bar job's", name)
		require.Equal(t, "${{ inputs.base_commit }}", j.Env["BASE_COMMIT"], name)
		require.Equal(t, "${{ inputs.expected_tree }}", j.Env["EXPECTED_TREE"], name)
		var checkout *rbStep
		for i := range j.Steps {
			if strings.HasPrefix(j.Steps[i].Uses, "actions/checkout@") {
				require.Nil(t, checkout, "%s: more than one checkout", name)
				checkout = &j.Steps[i]
			}
		}
		require.NotNil(t, checkout, name)
		require.Equal(t, "${{ inputs.scratch_ref }}", checkout.With["ref"], name)
		require.Equal(t, "0", checkout.With["fetch-depth"], name)
		require.Equal(t, "false", checkout.With["persist-credentials"], name)
	}
}
