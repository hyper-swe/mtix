// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.17.42: scripts/main-macos-health.sh blocks merges while main's
// macOS CI is red. The tests run the real script against a stub gh that serves
// per-run fixtures, so they prove WHICH run is judged and which API calls are
// made, not only the verdict. Policy under test: fail closed on any API error
// or unreadable state; every expected macOS job must be present and success;
// a first-attempt run in progress is not judged; a re-run in progress is
// judged by its previous attempt; a cancelled run is red if a macOS job failed
// and is otherwise skipped for at most three runs; runs of other events never
// count; the label main-macos-fix overrides.
package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const healthStub = `#!/bin/bash
echo "$*" >> "$FIX/requests.log"
if [ "$1" = pr ]; then echo '[]'; exit 0; fi
url=""; pag=0
for a in "$@"; do case "$a" in --paginate) pag=1 ;; repos/*) url=$a ;; esac; done
case "$url" in
  */workflows/ci.yml/runs*) f=runs; want="per_page=30" ;;
  */attempts/*/jobs*) n=${url##*/attempts/}; n=${n%%/*}; id=${url#*/runs/}; id=${id%%/*}; f=jobs-$id-a$n; want="per_page=100" ;;
  *) echo "unexpected url $url" >&2; exit 1 ;;
esac
case "$url" in *"$want"*) ;; *) echo "bad paging in $url" >&2; exit 1 ;; esac
[ -f "$FIX/$f.err" ] && { [ -f "$FIX/$f.json" ] && cat "$FIX/$f.json"; cat "$FIX/$f.err" >&2; exit 1; }
[ -f "$FIX/$f.json" ] || { echo "HTTP 404 $f" >&2; exit 1; }
if [ "$f" != runs ] && [ "$pag" = 0 ]; then head -n 3 "$FIX/$f.json"; else cat "$FIX/$f.json"; fi
`

var healthExpected = []string{
	"test-go-shard (macos-latest, other)",
	"test-go-sqlite (macos-latest, shard 1)", "test-go-sqlite (macos-latest, shard 2)", "test-go-sqlite (macos-latest, shard 3)", "test-go-sqlite (macos-latest, shard 4)",
	"test-go-cmd (macos-latest, shard 1)", "test-go-cmd (macos-latest, shard 2)", "test-go-cmd (macos-latest, shard 3)",
	"test-go-service (macos-latest, shard 1)", "test-go-service (macos-latest, shard 2)",
}

// allExpectedJobs maps every expected macOS job to the given conclusion.
func allExpectedJobs(conclusion string) map[string]string {
	m := map[string]string{}
	for _, name := range healthExpected {
		m[name] = conclusion
	}
	return m
}

func hJob(name, conclusion string) string {
	return fmt.Sprintf(`{"name":%q,"conclusion":%q}`, name, conclusion)
}

// hJobs builds a jobs stream: Linux jobs first, then the expected macOS jobs
// with the given overrides (name suffix -> conclusion), the last job last.
func hJobs(overrides map[string]string, drop ...string) string {
	lines := []string{hJob("test-go-cmd (ubuntu-22.04, shard 1)", "success"), hJob("test-go (x)", "success"), hJob("lint", "success")}
	for _, name := range healthExpected {
		skip := false
		for _, d := range drop {
			skip = skip || d == name
		}
		if skip {
			continue
		}
		c := "success"
		if o, ok := overrides[name]; ok {
			c = o
		}
		lines = append(lines, hJob(name, c))
	}
	return strings.Join(lines, "\n") + "\n"
}

// hJobsAttempt marks every row of a jobs stream with the given run_attempt, so
// fixtures can mix rows of two attempts the way the jobs API sometimes does.
func hJobsAttempt(stream string, attempt int) string {
	return strings.ReplaceAll(stream, `"conclusion"`, fmt.Sprintf(`"run_attempt":%d,"conclusion"`, attempt))
}

type hRun struct {
	id, event, status, conclusion string
	attempt                       int
}

func hRuns(runs ...hRun) string {
	var items []string
	for _, r := range runs {
		a := r.attempt
		if a == 0 {
			a = 1
		}
		items = append(items, fmt.Sprintf(`{"id":%s,"event":%q,"status":%q,"conclusion":%s,"run_attempt":%d}`,
			r.id, r.event, r.status, jsonOrNull(r.conclusion), a))
	}
	return `{"workflow_runs":[` + strings.Join(items, ",") + "]}"
}

func jsonOrNull(s string) string {
	if s == "" {
		return "null"
	}
	return fmt.Sprintf("%q", s)
}

type hResult struct {
	out      string
	err      error
	requests string
	tmpLeft  int
}

func runHealth(t *testing.T, files map[string]string, labels string, args ...string) hResult {
	t.Helper()
	fix, bin, tmp := t.TempDir(), t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(healthStub), 0o700))
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(fix, name), []byte(body), 0o600))
	}
	cmd := exec.Command("bash", append([]string{"scripts/main-macos-health.sh"}, args...)...)
	cmd.Dir = ".." // both real callers run it from the repository root
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FIX="+fix, "TMPDIR="+tmp,
		"GITHUB_REPOSITORY=example/repo", "PR_LABELS="+labels)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("main-macos-health.sh hung")
	}
	reqs, reqErr := os.ReadFile(filepath.Join(fix, "requests.log"))
	if reqErr != nil {
		require.ErrorIs(t, reqErr, os.ErrNotExist, "a failed read must not look like no requests")
	}
	left, readErr := os.ReadDir(tmp)
	require.NoError(t, readErr)
	return hResult{string(out), err, string(reqs), len(left)}
}

type healthCase struct {
	name      string
	files     map[string]string
	wantPass  bool
	wantText  string
	wantJudge string // run id that must be fetched
	notJudge  string // run id whose jobs must NOT be fetched
}

func healthCases() []healthCase {
	green, push := hJobs(nil), func(id, st, c string, a int) hRun { return hRun{id, "push", st, c, a} }
	red := hJobs(map[string]string{"test-go-cmd (macos-latest, shard 3)": "failure"})
	return []healthCase{
		{"green newest run", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": green}, true, "judged main run 11", "11", ""},
		{"newest red, older green: newest decides", map[string]string{"runs.json": hRuns(push("11", "completed", "failure", 1), push("9", "completed", "success", 1)), "jobs-11-a1.json": red, "jobs-9-a1.json": green}, false, "test-go-cmd (macos-latest, shard 3)=failure", "11", "9"},
		{"newest green, older red: newest decides", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1), push("9", "completed", "failure", 1)), "jobs-11-a1.json": green, "jobs-9-a1.json": red}, true, "judged main run 11", "11", "9"},
		{"red Linux, green macOS passes", map[string]string{"runs.json": hRuns(push("11", "completed", "failure", 1)), "jobs-11-a1.json": strings.Replace(green, `"success"`, `"failure"`, 1)}, true, "judged main run 11", "11", ""},
		{"scheduled run counts", map[string]string{"runs.json": hRuns(hRun{"11", "schedule", "completed", "success", 1}), "jobs-11-a1.json": green}, true, "judged main run 11", "11", ""},
		{"cancelled macOS job", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": hJobs(map[string]string{"test-go-cmd (macos-latest, shard 1)": "cancelled"})}, false, "shard 1)=cancelled", "11", ""},
		{"timed out macOS job", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": hJobs(map[string]string{"test-go-cmd (macos-latest, shard 1)": "timed_out"})}, false, "=timed_out", "11", ""},
		{"skipped macOS job is red", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": hJobs(map[string]string{"test-go-cmd (macos-latest, shard 1)": "skipped"})}, false, "=skipped", "11", ""},
		{"all macOS jobs skipped is red", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": hJobs(allExpectedJobs("skipped"))}, false, "=skipped", "11", ""},
		{"missing one expected job is red", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": hJobs(nil, healthExpected[7])}, false, "missing expected macOS jobs: test-go-cmd (macos-latest, shard 3)", "11", ""},
		{"no macOS jobs is red", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": hJob("lint", "success") + "\n"}, false, "missing expected macOS jobs", "11", ""},
		{"unexpected macOS job red", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": hJobs(nil) + hJob("extra (macos-14)", "failure") + "\n"}, false, "extra (macos-14)=failure", "11", ""},
		{"first-attempt run in progress is ignored", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 1), push("11", "completed", "success", 1)), "jobs-11-a1.json": green, "jobs-12-a1.json": red}, true, "judged main run 11", "11", "12"},
		{"queued first attempt is ignored", map[string]string{"runs.json": hRuns(push("12", "queued", "", 1), push("11", "completed", "success", 1)), "jobs-11-a1.json": green}, true, "judged main run 11", "11", "12"},
		{"re-run in progress over red attempt fails closed", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 2), push("11", "completed", "success", 1)), "jobs-12-a1.json": red, "jobs-11-a1.json": green}, false, "test-go-cmd (macos-latest, shard 3)=failure", "12", "11"},
		{"re-run in progress over green attempt is judged by it", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 2)), "jobs-12-a1.json": green}, true, "attempt 1 of a re-run", "12", ""},
		{"cancelled run with failed macOS job is red", map[string]string{"runs.json": hRuns(push("12", "completed", "cancelled", 1), push("11", "completed", "success", 1)), "jobs-12-a1.json": red, "jobs-11-a1.json": green}, false, "run 12 (cancelled run)", "12", "11"},
		{"cancelled run without failure is skipped", map[string]string{"runs.json": hRuns(push("12", "completed", "cancelled", 1), push("11", "completed", "success", 1)), "jobs-12-a1.json": hJobs(map[string]string{healthExpected[0]: "cancelled"}), "jobs-11-a1.json": green}, true, "judged main run 11", "11", ""},
		{"three cancelled runs then green passes", map[string]string{"runs.json": hRuns(push("14", "completed", "cancelled", 1), push("13", "completed", "cancelled", 1), push("12", "completed", "cancelled", 1), push("11", "completed", "success", 1)), "jobs-14-a1.json": green, "jobs-13-a1.json": green, "jobs-12-a1.json": green, "jobs-11-a1.json": green}, true, "judged main run 11", "11", ""},
		{"four cancelled runs fail closed", map[string]string{"runs.json": hRuns(push("15", "completed", "cancelled", 1), push("14", "completed", "cancelled", 1), push("13", "completed", "cancelled", 1), push("12", "completed", "cancelled", 1), push("11", "completed", "success", 1)), "jobs-15-a1.json": green, "jobs-14-a1.json": green, "jobs-13-a1.json": green, "jobs-12-a1.json": green, "jobs-11-a1.json": green}, false, "consecutive cancelled", "12", "11"},
		{"pull_request and dispatch runs never count", map[string]string{"runs.json": hRuns(hRun{"13", "pull_request", "completed", "failure", 1}, hRun{"12", "workflow_dispatch", "completed", "failure", 1}, push("11", "completed", "success", 1)), "jobs-11-a1.json": green}, true, "judged main run 11", "11", "13"},
		{"partial first page: red job on a later page is seen", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": red}, false, "=failure", "11", ""},
		{"no runs fails closed", map[string]string{"runs.json": `{"workflow_runs":[]}`}, false, "no completed main CI run", "", ""},
		{"only in-progress runs fails closed", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 1))}, false, "no completed main CI run", "", "12"},
		{"runs api error fails closed", map[string]string{"runs.err": "HTTP 502 Bad Gateway"}, false, "HTTP 502 Bad Gateway", "", ""},
		{"jobs api error fails closed", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.err": "HTTP 403 rate limit"}, false, "HTTP 403 rate limit", "11", ""},
		{"jobs api error after a partial page fails closed", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": hJobs(nil), "jobs-11-a1.err": "HTTP 502 mid-pagination"}, false, "HTTP 502 mid-pagination", "11", ""},
		{"cancelled re-run of a red run stays red", map[string]string{"runs.json": hRuns(push("12", "completed", "cancelled", 2), push("11", "completed", "success", 1)), "jobs-12-a2.json": hJobs(map[string]string{healthExpected[1]: "cancelled"}), "jobs-12-a1.json": red, "jobs-11-a1.json": green}, false, "test-go-cmd (macos-latest, shard 3)=failure", "12", "11"},
		{"cancelled re-run over a green attempt is judged by it", map[string]string{"runs.json": hRuns(push("12", "completed", "cancelled", 2)), "jobs-12-a2.json": hJobs(map[string]string{healthExpected[1]: "cancelled"}), "jobs-12-a1.json": green}, true, "attempt 1", "12", ""},
		{"attempt 3 in progress judges attempt 2, not attempt 1", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 3)), "jobs-12-a2.json": red, "jobs-12-a1.json": green}, false, "=failure", "12", ""},
		{"cancelled attempt is walked past to an older red attempt", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 3)), "jobs-12-a2.json": hJobs(map[string]string{healthExpected[1]: "cancelled"}), "jobs-12-a1.json": red}, false, "=failure", "12", ""},
		{"no readable earlier attempt fails closed", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 2)), "jobs-12-a1.err": "HTTP 404"}, false, "HTTP 404", "12", ""},
		{"every earlier attempt cancelled fails closed", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 2)), "jobs-12-a1.json": hJobs(map[string]string{healthExpected[1]: "cancelled"})}, false, "no attempt", "12", ""},
		{"cancelled run with timed out macOS job is red", map[string]string{"runs.json": hRuns(push("12", "completed", "cancelled", 1), push("11", "completed", "success", 1)), "jobs-12-a1.json": hJobs(map[string]string{healthExpected[2]: "timed_out"}), "jobs-11-a1.json": green}, false, "=timed_out", "12", "11"},
		{"cancelled run with red Linux only is skipped", map[string]string{"runs.json": hRuns(push("12", "completed", "cancelled", 1), push("11", "completed", "success", 1)), "jobs-12-a1.json": strings.Replace(green, `"success"`, `"failure"`, 1), "jobs-11-a1.json": green}, true, "judged main run 11", "11", ""},
		{"jobs of two attempts: green re-run beside a red attempt 1 row", map[string]string{"runs.json": hRuns(push("12", "completed", "success", 2)), "jobs-12-a2.json": hJobsAttempt(red, 1) + hJobsAttempt(green, 2)}, true, "judged main run 12", "12", ""},
		{"jobs of two attempts: red re-run beside a green attempt 1 row", map[string]string{"runs.json": hRuns(push("12", "completed", "failure", 2)), "jobs-12-a2.json": hJobsAttempt(green, 1) + hJobsAttempt(red, 2)}, false, "=failure", "12", ""},
		{"cancelled re-run with its own failed job is red even over a green attempt 1", map[string]string{"runs.json": hRuns(push("12", "completed", "cancelled", 2)), "jobs-12-a2.json": red, "jobs-12-a1.json": green}, false, "run 12 (cancelled re-run)", "12", ""},
		{"dedupe: higher attempt rows listed first, green re-run", map[string]string{"runs.json": hRuns(push("12", "completed", "success", 2)), "jobs-12-a2.json": hJobsAttempt(green, 2) + hJobsAttempt(red, 1)}, true, "judged main run 12", "12", ""},
		{"dedupe: higher attempt rows listed first, red re-run", map[string]string{"runs.json": hRuns(push("12", "completed", "failure", 2)), "jobs-12-a2.json": hJobsAttempt(red, 2) + hJobsAttempt(green, 1)}, false, "=failure", "12", ""},
		{"walk: failure plus cancelled in attempt 2 is red over green attempt 1", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 3)), "jobs-12-a2.json": hJobs(map[string]string{healthExpected[1]: "cancelled", healthExpected[3]: "failure"}), "jobs-12-a1.json": green}, false, "=failure", "12", ""},
		{"walk: missing job in attempt 2 is red over green attempt 1", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 3)), "jobs-12-a2.json": hJobs(nil, healthExpected[7]), "jobs-12-a1.json": green}, false, "missing expected macOS jobs", "12", ""},
		{"walk: skipped job in attempt 2 is red over green attempt 1", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 3)), "jobs-12-a2.json": hJobs(map[string]string{healthExpected[1]: "skipped"}), "jobs-12-a1.json": green}, false, "=skipped", "12", ""},
		{"garbage runs fails closed", map[string]string{"runs.json": `not json`}, false, "cannot read main CI state", "", ""},
		{"empty runs fails closed", map[string]string{"runs.json": ``}, false, "cannot read main CI state", "", ""},
		{"null workflow_runs fails closed", map[string]string{"runs.json": `{"workflow_runs":null}`}, false, "cannot read main CI state", "", ""},
		{"empty jobs fails closed", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": ``}, false, "cannot read jobs", "11", ""},
		{"garbage jobs fails closed", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11-a1.json": "not json\n"}, false, "cannot read jobs", "11", ""},
	}
}

func TestMainMacOSHealth_Decisions_BindJobsToTheJudgedRun(t *testing.T) {
	for _, tt := range healthCases() {
		t.Run(tt.name, func(t *testing.T) {
			r := runHealth(t, tt.files, `[]`)
			if tt.wantPass {
				require.NoError(t, r.err, r.out)
			} else {
				require.Error(t, r.err, r.out)
			}
			require.Contains(t, r.out, tt.wantText)
			if tt.wantJudge != "" {
				require.Contains(t, r.requests, "actions/runs/"+tt.wantJudge+"/", "judged run must be fetched")
			}
			if tt.notJudge != "" {
				require.NotContains(t, r.requests, "actions/runs/"+tt.notJudge+"/", "run must not be fetched")
			}
			require.Zero(t, r.tmpLeft, "temporary directory must be removed")
		})
	}
}

func TestMainMacOSHealth_Override_ReadsExactLabelAndSkipsTheAPI(t *testing.T) {
	for _, tt := range []struct {
		labels   string
		wantPass bool
	}{
		{`["main-macos-fix"]`, true}, {`["bug","main-macos-fix"]`, true},
		{`["bug"]`, false}, {`["Main-Macos-Fix"]`, false}, {`["main-macos-fix-2"]`, false},
		{`oops`, false}, {`"main-macos-fix"`, false}, {``, false},
	} {
		r := runHealth(t, map[string]string{"runs.err": "HTTP 500"}, tt.labels)
		if tt.wantPass {
			require.NoError(t, r.err, r.out)
			require.Contains(t, r.out, "override")
			require.Empty(t, r.requests, "an override must not depend on the API")
		} else {
			require.Error(t, r.err, tt.labels+r.out)
		}
	}
}

func TestMainMacOSHealth_Arguments_AreRejectedWithoutHanging(t *testing.T) {
	for _, args := range [][]string{{"--runs"}, {"--jobs", "x"}, {"unexpected"}} {
		r := runHealth(t, map[string]string{"runs.json": hRuns(hRun{"11", "push", "completed", "success", 1}), "jobs-11-a1.json": hJobs(nil)}, `[]`, args...)
		require.Error(t, r.err, r.out)
		require.Contains(t, r.out, "takes no arguments")
		require.Empty(t, r.requests)
	}
}

func TestMainMacOSHealth_Repository_UnsetFailsClosed(t *testing.T) {
	cmd := exec.Command("bash", "scripts/main-macos-health.sh")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "GITHUB_REPOSITORY=", "PR_LABELS=[]")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))
	require.Contains(t, string(out), "GITHUB_REPOSITORY is unset")
}

func TestMainMacOSHealth_Requests_ReadTheRunAttemptEndpoint(t *testing.T) {
	r := runHealth(t, map[string]string{"runs.json": hRuns(hRun{"12", "push", "completed", "success", 2}), "jobs-12-a2.json": hJobs(nil)}, `[]`)
	require.NoError(t, r.err, r.out)
	require.Contains(t, r.requests, "actions/runs/12/attempts/2/jobs?per_page=100")
	require.NotContains(t, r.requests, "filter=")
}

func TestMainMacOSHealth_ExpectedList_MissingOrEmptyFailsClosed(t *testing.T) {
	script, err := os.ReadFile("main-macos-health.sh")
	require.NoError(t, err)
	for name, list := range map[string]*string{"missing": nil, "empty": new(string)} {
		dir := filepath.Join(t.TempDir(), "scripts")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "main-macos-health.sh"), script, 0o700))
		if list != nil {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "main-macos-expected-jobs.txt"), []byte(*list), 0o600))
		}
		cmd := exec.Command("bash", filepath.Join(dir, "main-macos-health.sh"))
		cmd.Env = append(os.Environ(), "GITHUB_REPOSITORY=example/repo", "PR_LABELS=[]")
		out, runErr := cmd.CombinedOutput()
		require.Error(t, runErr, name+": "+string(out))
		require.Contains(t, string(out), "expected macOS job list", name)
	}
}

// mergeGate extracts the exact merge-time command from docs/CI-GATES.md.
func mergeGate(t *testing.T) string {
	t.Helper()
	doc, err := os.ReadFile("../docs/CI-GATES.md")
	require.NoError(t, err)
	_, after, ok := strings.Cut(string(doc), "<!-- merge-gate-command -->")
	require.True(t, ok, "docs must carry the merge-time command under its marker")
	_, block, ok := strings.Cut(after, "```\n")
	require.True(t, ok)
	block, _, _ = strings.Cut(block, "```")
	var lines []string
	for _, line := range strings.Split(block, "\n") {
		lines = append(lines, strings.TrimSpace(line))
	}
	command := strings.Join(lines, "\n")
	require.NotContains(t, command, "$PWD")
	return strings.ReplaceAll(command, "<PR>", "1")
}

func mergeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	require.NoError(t, err, string(out))
}

// mergeCommit writes the gate script and the expected list into repo and commits.
func mergeCommit(t *testing.T, repo, script string) {
	t.Helper()
	list, err := os.ReadFile("main-macos-expected-jobs.txt")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "scripts"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "main-macos-health.sh"), []byte(script), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "main-macos-expected-jobs.txt"), list, 0o600))
	mergeGit(t, repo, "add", "-A")
	mergeGit(t, repo, "commit", "-q", "-m", "c")
}

// mergeRun runs the merge command in dir with a stub gh and extra environment.
func mergeRun(t *testing.T, dir string, env ...string) (string, error) {
	t.Helper()
	fix, bin := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(healthStub), 0o700))
	cmd := exec.Command("bash", "-c", mergeGate(t))
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FIX="+fix), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const (
	stalePin = "#!/bin/bash\necho STALECOPY\nexit 0\n"
	freshPin = "#!/bin/bash\necho FRESHCOPY\nexit 3\n"
	hostile  = "#!/bin/bash\necho HOSTILECOPY\nexit 0\n"
)

// mergeSetup builds origin (stale script), a clone, then moves origin to the
// fresh script and leaves the clone on a branch that commits a hostile one.
func mergeSetup(t *testing.T) (origin, clone string) {
	t.Helper()
	origin, clone = t.TempDir(), filepath.Join(t.TempDir(), "clone")
	mergeGit(t, origin, "init", "-q", "-b", "main")
	mergeCommit(t, origin, stalePin)
	mergeGit(t, filepath.Dir(clone), "clone", "-q", origin, clone)
	mergeCommit(t, origin, freshPin)
	mergeGit(t, clone, "checkout", "-q", "-b", "pr")
	mergeCommit(t, clone, hostile)
	return origin, clone
}

// TestMainMacOSHealth_MergeCommand_RunsMainAsFetchedAtMergeTime executes the
// exact command from docs/CI-GATES.md in a clone whose local origin/main is
// stale and whose checked out branch commits a hostile script that exits 0:
// the command must fetch and run origin's current copy.
func TestMainMacOSHealth_MergeCommand_RunsMainAsFetchedAtMergeTime(t *testing.T) {
	_, clone := mergeSetup(t)
	out, err := mergeRun(t, clone)
	require.Error(t, err, out)
	require.Contains(t, out, "FRESHCOPY")
	require.NotContains(t, out, "STALECOPY")
	require.NotContains(t, out, "HOSTILECOPY")
}

func TestMainMacOSHealth_MergeCommand_NarrowedRefspecStillRunsFreshMain(t *testing.T) {
	_, clone := mergeSetup(t)
	mergeGit(t, clone, "config", "remote.origin.fetch", "+refs/heads/other:refs/remotes/origin/other")
	out, err := mergeRun(t, clone)
	require.Error(t, err, out)
	require.Contains(t, out, "FRESHCOPY")
	require.NotContains(t, out, "STALECOPY")
}

func TestMainMacOSHealth_MergeCommand_FailedFetchRunsNothingAndDeletesNothing(t *testing.T) {
	origin, clone := mergeSetup(t)
	mergeGit(t, clone, "fetch", "-q", "origin", "main") // leaves a usable FETCH_HEAD behind
	mergeGit(t, clone, "remote", "set-url", "origin", filepath.Join(origin, "gone"))
	keep := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(keep, "scripts"), 0o700))
	sentinel := filepath.Join(keep, "scripts", "main-macos-health.sh")
	require.NoError(t, os.WriteFile(sentinel, []byte("sentinel"), 0o600))
	out, err := mergeRun(t, clone, "d="+keep)
	require.Error(t, err, out)
	for _, marker := range []string{"STALECOPY", "FRESHCOPY", "HOSTILECOPY"} {
		require.NotContains(t, out, marker)
	}
	require.FileExists(t, sentinel, "a failed fetch must not clean up a caller's directory")
	// A failed fetch must leave no ref to archive: the earlier successful fetch's FETCH_HEAD is gone.
	probe := exec.Command("git", "rev-parse", "-q", "--verify", "FETCH_HEAD")
	probe.Dir = clone
	require.Error(t, probe.Run(), "a failed fetch must not leave a usable FETCH_HEAD")
}

// TestMainMacOSHealth_MergeCommand_RealScriptBlocksARedMain runs the real
// script through the command from a clone holding a hostile working copy.
func TestMainMacOSHealth_MergeCommand_RealScriptBlocksARedMain(t *testing.T) {
	script, err := os.ReadFile("main-macos-health.sh")
	require.NoError(t, err)
	origin, clone := t.TempDir(), filepath.Join(t.TempDir(), "clone")
	mergeGit(t, origin, "init", "-q", "-b", "main")
	mergeCommit(t, origin, string(script))
	mergeGit(t, filepath.Dir(clone), "clone", "-q", origin, clone)
	require.NoError(t, os.WriteFile(filepath.Join(clone, "scripts", "main-macos-health.sh"), []byte(hostile), 0o700))
	fix, bin := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(healthStub), 0o700))
	red := hJobs(map[string]string{"test-go-cmd (macos-latest, shard 3)": "failure"})
	require.NoError(t, os.WriteFile(filepath.Join(fix, "runs.json"), []byte(hRuns(hRun{"11", "push", "completed", "failure", 1})), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(fix, "jobs-11-a1.json"), []byte(red), 0o600))
	cmd := exec.Command("bash", "-c", mergeGate(t))
	cmd.Dir = clone
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FIX="+fix)
	out, runErr := cmd.CombinedOutput()
	require.Error(t, runErr, string(out))
	require.Contains(t, string(out), "main is red on macOS in run 11")
}
