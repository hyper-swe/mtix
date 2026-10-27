// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.17.26: the remote bar reuses a green run for an identical tree,
// main base commit, workflow blob and options instead of dispatching again.
// Match data comes from the run-name that remote-bar.yml writes (matched as a
// whole against one anchored pattern, every field compared exactly), and every
// mismatch or lookup failure dispatches a fresh run.
package scripts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	reuseBlob  = "b10b0000000000000000000000000000000000aa"
	reuseOther = "b10b0000000000000000000000000000000000bb"
	hour       = time.Hour
)

// reuseKey is what a stub run's title is built from.
type reuseKey struct{ tree, base, runner, web, wrapper string }

func (k reuseKey) title() string {
	return fmt.Sprintf("remote-bar ci-scratch/OLD-20261001T000000Z-%s tree=%s base=%s os=%s web=%s wrapper=%s",
		k.tree[:8], k.tree, k.base, k.runner, k.web, k.wrapper)
}

// reuseRun renders one gh run list entry as the stub returns it.
func reuseRun(id int, title, concl, status string, age time.Duration) string {
	created := time.Now().Add(-age).UTC().Format(time.RFC3339)
	return fmt.Sprintf(`{"databaseId":%d,"displayTitle":%q,"conclusion":%q,"status":%q,"createdAt":%q,"headSha":"abc%d","url":"https://github.com/hyper-swe/mtix/actions/runs/%d"}`,
		id, title, concl, status, created, id, id)
}

func wrapperVersion(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(scriptPath)
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^RB_VERSION="([^"]+)"`).FindSubmatch(b)
	require.NotNil(t, m)
	return string(m[1])
}

// reuseCase runs the wrapper once against the stub. mk builds the listed runs
// from the real key (tree, base) of this snapshot.
type reuseCase struct {
	args     string
	withFlow bool // snapshot changes .github
	env      []string
	mk       func(k reuseKey) []string
}

func (c reuseCase) run(t *testing.T) (out string, code int, logged string) {
	t.Helper()
	work, _ := repoWithRemote(t)
	bin, logPath := stubGh(t)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(snap+"/b.txt", []byte("b"), 0o600))
	if c.withFlow {
		require.NoError(t, os.MkdirAll(snap+"/.github", 0o750))
		require.NoError(t, os.WriteFile(snap+"/.github/x.yml", []byte("x"), 0o600))
	}
	pathEnv := "PATH=" + bin + ":" + os.Getenv("PATH")
	dry, dcode := bash(t, work, `bash "$RB_SCRIPT" --dry-run --allow-workflow-diff "$S"`, "S="+snap, pathEnv)
	require.Equal(t, 0, dcode, dry)
	m := regexp.MustCompile(`tree=([0-9a-f]{40}) base=([0-9a-f]{40})`).FindStringSubmatch(dry)
	require.NotNil(t, m, dry)
	key := reuseKey{tree: m[1], base: m[2], runner: "ubuntu-22.04", web: "false", wrapper: wrapperVersion(t)}
	var runs []string
	if c.mk != nil {
		runs = c.mk(key)
	}
	snippet := `source "$RB_SCRIPT"; rb_remote_urls_ok() { return 0; }; rb_install_traps; rb_run --ticket MTIX-1 ` + c.args + ` "$S"`
	all := append([]string{"S=" + snap, "RB_REMOTE=origin", "STUB_LOG=" + logPath, "RB_POLL_SECS=1",
		"STUB_MAIN_BLOB=" + reuseBlob, "STUB_RUN_BLOB=" + reuseBlob, "STUB_STATE=completed success",
		"STUB_REUSE_RUNS=[" + strings.Join(runs, ",") + "]", pathEnv}, c.env...)
	out, code = bash(t, work, snippet, all...)
	b, err := os.ReadFile(logPath) //nolint:gosec // test-owned path
	require.NoError(t, err)
	return out, code, string(b)
}

// one returns a mk that lists a single run built from the key after edit.
func one(edit func(k *reuseKey), concl, status string, age time.Duration) func(reuseKey) []string {
	return func(k reuseKey) []string {
		edit(&k)
		return []string{reuseRun(1, k.title(), concl, status, age)}
	}
}

func same(*reuseKey) {}

func TestRemoteBar_Reuse_IdenticalGreenRunIsReusedWithoutDispatch(t *testing.T) {
	out, code, logged := reuseCase{mk: one(same, "success", "completed", hour)}.run(t)
	require.Equal(t, 0, code, out)
	for _, want := range []string{"REUSED", "actions/runs/1 ", "tree=", "base=", "os=ubuntu-22.04", "web=false", "verdict=REUSED"} {
		require.Contains(t, out, want)
	}
	require.NotContains(t, logged, "workflow run", "no dispatch on reuse")
	require.NotContains(t, logged, "push", "nothing is pushed")
}

func TestRemoteBar_Reuse_EveryMismatchDispatchesFresh(t *testing.T) {
	a40 := strings.Repeat("a", 40)
	cases := []struct {
		name string
		c    reuseCase
	}{
		{"different tree", reuseCase{mk: one(func(k *reuseKey) { k.tree = a40 }, "success", "completed", hour)}},
		{"different main base", reuseCase{mk: one(func(k *reuseKey) { k.base = a40 }, "success", "completed", hour)}},
		{"different os", reuseCase{mk: one(func(k *reuseKey) { k.runner = "macos-latest" }, "success", "completed", hour)}},
		{"different web", reuseCase{mk: one(func(k *reuseKey) { k.web = "true" }, "success", "completed", hour)}},
		{"web requested", reuseCase{args: "--web", mk: one(same, "success", "completed", hour)}},
		{"different wrapper", reuseCase{mk: one(func(k *reuseKey) { k.wrapper = "0" }, "success", "completed", hour)}},
		{"different workflow blob", reuseCase{env: []string{"STUB_RUN_BLOB=" + reuseOther}, mk: one(same, "success", "completed", hour)}},
		{"run blob lookup fails", reuseCase{env: []string{"STUB_RUN_BLOB="}, mk: one(same, "success", "completed", hour)}},
		{"main blob lookup fails", reuseCase{env: []string{"STUB_MAIN_BLOB=", "STUB_RUN_BLOB="}, mk: one(same, "success", "completed", hour)}},
		{"failed run", reuseCase{mk: one(same, "failure", "completed", hour)}},
		{"cancelled run", reuseCase{mk: one(same, "cancelled", "completed", hour)}},
		{"in progress run", reuseCase{mk: one(same, "", "in_progress", hour)}},
		{"queued run", reuseCase{mk: one(same, "", "queued", hour)}},
		{"too old by default", reuseCase{mk: one(same, "success", "completed", 13*hour)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code, logged := tc.c.run(t)
			require.Equal(t, 0, code, out)
			require.NotContains(t, out, "REUSED")
			require.Contains(t, logged, "workflow run remote-bar.yml", "a fresh run is dispatched")
		})
	}
}

// A run whose status says in progress is not reused even when its conclusion
// field reads success (the status guard stands on its own).
func TestRemoteBar_Reuse_InProgressStatusAloneExcludesARun(t *testing.T) {
	out, code, logged := reuseCase{mk: one(same, "success", "in_progress", hour)}.run(t)
	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "REUSED")
	require.Contains(t, logged, "workflow run")
}

func TestRemoteBar_Reuse_ForgedTitlesAreNeverReused(t *testing.T) {
	a40 := strings.Repeat("a", 40)
	forged := map[string]func(k reuseKey) string{
		"second tree injected": func(k reuseKey) string {
			return strings.Replace(k.title(), "tree="+k.tree, "tree="+a40+" tree="+k.tree, 1)
		},
		"tree smuggled via wrapper": func(k reuseKey) string {
			j := k
			j.tree = a40
			return strings.Replace(j.title(), "wrapper="+k.wrapper, "wrapper=9 tree="+k.tree+" base="+k.base+" os=ubuntu-22.04 web=false wrapper="+k.wrapper, 1)
		},
		"branch name carries the key": func(k reuseKey) string {
			j := k
			j.tree = a40
			return strings.Replace(j.title(), "ci-scratch/", "ci-scratch/x tree="+k.tree+" base="+k.base+" os=ubuntu-22.04 web=false wrapper="+k.wrapper+" ", 1)
		},
		"empty base":        func(k reuseKey) string { return strings.Replace(k.title(), "base="+k.base, "base=", 1) },
		"placeholder base":  func(k reuseKey) string { return strings.Replace(k.title(), "base="+k.base, "base=unknown", 1) },
		"trailing junk":     func(k reuseKey) string { return k.title() + " extra" },
		"leading junk":      func(k reuseKey) string { return "x " + k.title() },
		"upper case tree":   func(k reuseKey) string { return strings.Replace(k.title(), k.tree, strings.ToUpper(k.tree), 1) },
		"os with wildcard":  func(k reuseKey) string { return strings.Replace(k.title(), "ubuntu-22.04", "ubuntu-22x04", 1) },
		"short tree prefix": func(k reuseKey) string { return strings.Replace(k.title(), k.tree, k.tree[:39], 1) },
	}
	for name, f := range forged {
		t.Run(name, func(t *testing.T) {
			mk := func(k reuseKey) []string { return []string{reuseRun(1, f(k), "success", "completed", hour)} }
			out, code, logged := reuseCase{mk: mk}.run(t)
			require.Equal(t, 0, code, out)
			require.NotContains(t, out, "REUSED")
			require.Contains(t, logged, "workflow run")
		})
	}
}

func TestRemoteBar_Reuse_AgeLimitDefaultsToTwelveHoursAndIsConfigurable(t *testing.T) {
	out, code, logged := reuseCase{mk: one(same, "success", "completed", 11*hour)}.run(t)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "REUSED")
	require.NotContains(t, logged, "workflow run")
	run := one(same, "success", "completed", 2*hour)
	out, code, logged = reuseCase{env: []string{"RB_REUSE_MAX_AGE_SECS=3600"}, mk: run}.run(t)
	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "REUSED")
	require.Contains(t, logged, "workflow run")
	out, code, _ = reuseCase{env: []string{"RB_REUSE_MAX_AGE_SECS=10800"}, mk: run}.run(t)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "REUSED")
}

func TestRemoteBar_Reuse_MaxAgeMustBeAPositiveInteger(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-5", "1.5", "1h", "1;id", "01x"} {
		t.Run(bad, func(t *testing.T) {
			work, _ := repoWithRemote(t)
			out, code := bash(t, work, `bash "$RB_SCRIPT" --dry-run "$S"`, "S="+t.TempDir(), "RB_REUSE_MAX_AGE_SECS="+bad)
			require.Equal(t, 2, code, out)
			require.Contains(t, out, "RB_REUSE_MAX_AGE_SECS must be a positive integer")
		})
	}
}

func TestRemoteBar_Reuse_NoReuseForcesAFreshRunWithoutLookup(t *testing.T) {
	out, code, logged := reuseCase{args: "--no-reuse", mk: one(same, "success", "completed", hour)}.run(t)
	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "REUSED")
	require.Contains(t, logged, "workflow run")
	require.NotContains(t, logged, "--status success", "no lookup when reuse is off")
}

// With a workflow diff, the lookup would have found a matching run; the flag
// must still force a fresh dispatch.
func TestRemoteBar_Reuse_WorkflowDiffReachesTheReusePointAndDispatchesFresh(t *testing.T) {
	out, code, logged := reuseCase{args: "--allow-workflow-diff", withFlow: true, mk: one(same, "success", "completed", hour)}.run(t)
	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "REUSED")
	require.Contains(t, logged, "workflow run")
	require.NotContains(t, logged, "--status success")
	require.Contains(t, out, "WARNING snapshot changes .github")
}

func TestRemoteBar_Reuse_DispatchSendsBaseAndFallsBackWhenMainRejectsIt(t *testing.T) {
	_, code, logged := reuseCase{}.run(t)
	require.Equal(t, 0, code)
	require.Contains(t, logged, "-f base_commit=")
	out, code, logged := reuseCase{env: []string{"STUB_REJECT_BASE=1"}}.run(t)
	require.Equal(t, 0, code, out)
	require.Equal(t, 2, strings.Count(logged, "workflow run remote-bar.yml"), "retried once without base_commit")
}

func TestRemoteBar_Reuse_OnlyTheUnknownInputRejectionFallsBack(t *testing.T) {
	out, code, logged := reuseCase{env: []string{"STUB_REJECT_BASE=1"}}.run(t)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "dispatching without a base")
	require.Contains(t, out, "not reusable")
	require.Equal(t, 2, strings.Count(logged, "workflow run remote-bar.yml"))
	out, code, logged = reuseCase{env: []string{"STUB_FAIL_DISPATCH=1"}}.run(t)
	require.Equal(t, 2, code, out)
	require.Contains(t, out, "dispatch failed")
	require.NotContains(t, out, "dispatching without a base")
	require.Equal(t, 1, strings.Count(logged, "workflow run remote-bar.yml"), "a generic failure is not retried")
}

func TestRemoteBar_Reuse_FreshRunKeepsExitCodes(t *testing.T) {
	out, code, _ := reuseCase{env: []string{"STUB_STATE=completed failure", "STUB_STEPS=Run committed bar"}}.run(t)
	require.Equal(t, 1, code, out)
	out, code, _ = reuseCase{env: []string{"STUB_STATE=completed failure", "STUB_STEPS=Verify snapshot tree"}}.run(t)
	require.Equal(t, 3, code, out)
}

func TestRemoteBarWorkflow_RunName_CarriesTreeBaseOsWebAndWrapper(t *testing.T) {
	b, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	want := "run-name: remote-bar ${{ inputs.scratch_ref }} tree=${{ inputs.expected_tree }} base=${{ inputs.base_commit }} os=${{ inputs.os }} web=${{ inputs.web }} wrapper=${{ inputs.wrapper_version }}"
	require.Contains(t, string(b), want)
}

func validateEnv(over map[string]string) []string {
	env := map[string]string{
		"SCRATCH_REF": "ci-scratch/MTIX-1-20261001T000000Z-abcdef01", "EXPECTED_TREE": strings.Repeat("a", 40),
		"BASE_COMMIT": strings.Repeat("b", 40), "RUN_OS": "ubuntu-22.04", "RUN_WEB": "false", "WRAPPER_VERSION": "2",
	}
	for k, v := range over {
		env[k] = v
	}
	var out []string
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func TestRemoteBarWorkflow_ValidateStep_AcceptsOnlyWellFormedWholeValues(t *testing.T) {
	run := stepRun(t, readWorkflow(t), "Validate run metadata")
	out, code := bash(t, t.TempDir(), run, validateEnv(nil)...)
	require.Equal(t, 0, code, out)
	out, code = bash(t, t.TempDir(), run, validateEnv(map[string]string{"BASE_COMMIT": ""})...)
	require.Equal(t, 0, code, "an empty base is allowed (a dispatch without base_commit): "+out)
	bad := map[string]map[string]string{
		"malformed base":           {"BASE_COMMIT": "unknown"},
		"short base":               {"BASE_COMMIT": strings.Repeat("b", 39)},
		"multi-line base":          {"BASE_COMMIT": strings.Repeat("b", 40) + "\nextra"},
		"multi-line wrapper":       {"WRAPPER_VERSION": "2\ntree=" + strings.Repeat("c", 40)},
		"leading line wrapper":     {"WRAPPER_VERSION": "x\n2"},
		"carriage return wrapper":  {"WRAPPER_VERSION": "2\r"},
		"non digit wrapper":        {"WRAPPER_VERSION": "2a"},
		"multi-line tree":          {"EXPECTED_TREE": strings.Repeat("a", 40) + "\n"},
		"upper tree":               {"EXPECTED_TREE": strings.Repeat("A", 40)},
		"os outside set":           {"RUN_OS": "ubuntu-22x04"},
		"web outside set":          {"RUN_WEB": "yes"},
		"scratch ref with space":   {"SCRATCH_REF": "ci-scratch/x tree=" + strings.Repeat("a", 40)},
		"scratch ref multi-line":   {"SCRATCH_REF": "ci-scratch/x\ny"},
		"scratch ref wrong prefix": {"SCRATCH_REF": "main"},
	}
	for name, over := range bad {
		t.Run(name, func(t *testing.T) {
			out, code := bash(t, t.TempDir(), run, validateEnv(over)...)
			require.NotEqual(t, 0, code, out)
			require.Contains(t, out, "invalid run metadata")
		})
	}
}

// A snapshot whose parent is not the claimed base commit fails every job that
// checks the snapshot out, so a crafted dispatch can never go green.
func TestRemoteBarWorkflow_VerifyStep_FailsWhenTheParentIsNotTheClaimedBase(t *testing.T) {
	wf := readWorkflow(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main")
	write := func(c string) {
		require.NoError(t, os.WriteFile(filepath.Join(repo, "f"), []byte(c), 0o600))
		git(t, repo, "add", "-A")
		git(t, repo, "commit", "-m", c)
	}
	write("old")
	oldMain := git(t, repo, "rev-parse", "HEAD")
	write("new")
	newMain := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "-q", "-b", "snap", oldMain)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f"), []byte("snap"), 0o600))
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "snap")
	tree := git(t, repo, "rev-parse", "HEAD^{tree}")
	snapCommit := git(t, repo, "rev-parse", "HEAD")
	mergeTree := tree
	merge := git(t, repo, "commit-tree", mergeTree, "-p", snapCommit, "-p", newMain, "-m", "m")

	// every job's verify step carries the check (validate, bar, bar-cmd, bar-macos, web)
	require.Equal(t, 5, strings.Count(wf, "base mismatch: expected $BASE_COMMIT"))
	run := stepRun(t, wf, "Verify snapshot tree")
	out, code := bash(t, repo, run, "EXPECTED_TREE="+tree, "BASE_COMMIT="+oldMain)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "base verified")
	out, code = bash(t, repo, run, "EXPECTED_TREE="+tree, "BASE_COMMIT="+newMain)
	require.NotEqual(t, 0, code, "claims the current main but sits on an old parent: "+out)
	require.Contains(t, out, "base mismatch")
	out, code = bash(t, repo, run, "EXPECTED_TREE="+tree, "BASE_COMMIT=")
	require.Equal(t, 0, code, "no claimed base means no base check: "+out)
	git(t, repo, "checkout", "-q", merge)
	out, code = bash(t, repo, run, "EXPECTED_TREE="+mergeTree, "BASE_COMMIT="+newMain)
	require.NotEqual(t, 0, code, "two parents are refused: "+out)
	require.Contains(t, out, "exactly one parent")
}

func TestRemoteBarWorkflow_ValidateJobGatesAllJobsAndVerifiesTheSnapshot(t *testing.T) {
	wf := readWorkflow(t)
	for _, job := range []string{"bar", "bar-cmd", "bar-macos", "web"} {
		require.Regexp(t, regexp.MustCompile(`(?m)^  `+job+`:\n(    [^\n]*\n)*?    needs: validate\n`), wf, job)
	}
	require.Equal(t, 5, strings.Count(wf, "BASE_COMMIT: ${{ inputs.base_commit }}"), "every job that checks out the snapshot reads the claimed base")
}
