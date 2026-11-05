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
url=""; pag=0
for a in "$@"; do case "$a" in --paginate) pag=1 ;; repos/*) url=$a ;; esac; done
case "$url" in
  */workflows/ci.yml/runs*) f=runs; want="per_page=30" ;;
  */attempts/*/jobs*) n=${url##*/attempts/}; n=${n%%/*}; id=${url#*/runs/}; id=${id%%/*}; f=jobs-$id-a$n; want="per_page=100" ;;
  */runs/*/jobs*) id=${url#*/runs/}; id=${id%%/*}; f=jobs-$id; want="per_page=100" ;;
  *) echo "unexpected url $url" >&2; exit 1 ;;
esac
case "$url" in *"$want"*) ;; *) echo "bad paging in $url" >&2; exit 1 ;; esac
[ -f "$FIX/$f.err" ] && { [ -f "$FIX/$f.json" ] && cat "$FIX/$f.json"; cat "$FIX/$f.err" >&2; exit 1; }
[ -f "$FIX/$f.json" ] || { echo "HTTP 404 $f" >&2; exit 1; }
if [ "$f" != runs ] && [ "$pag" = 0 ]; then head -n 3 "$FIX/$f.json"; else cat "$FIX/$f.json"; fi
`

var healthExpected = []string{
	"test-go-shard (macos-latest, other)",
	"test-go-sqlite (macos-latest, shard 1)", "test-go-sqlite (macos-latest, shard 2)", "test-go-sqlite (macos-latest, shard 3)",
	"test-go-cmd (macos-latest, shard 1)", "test-go-cmd (macos-latest, shard 2)", "test-go-cmd (macos-latest, shard 3)",
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
	cmd := exec.Command("bash", append([]string{"main-macos-health.sh"}, args...)...)
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
	reqs, _ := os.ReadFile(filepath.Join(fix, "requests.log"))
	left, _ := os.ReadDir(tmp)
	return hResult{string(out), err, string(reqs), len(left)}
}

func TestMainMacOSHealth_Decisions_BindJobsToTheJudgedRun(t *testing.T) {
	green, push := hJobs(nil), func(id, st, c string, a int) hRun { return hRun{id, "push", st, c, a} }
	red := hJobs(map[string]string{"test-go-cmd (macos-latest, shard 3)": "failure"})
	tests := []struct {
		name      string
		files     map[string]string
		wantPass  bool
		wantText  string
		wantJudge string // run id that must be fetched
		notJudge  string // run id whose jobs must NOT be fetched
	}{
		{"green newest run", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": green}, true, "judged main run 11", "11", ""},
		{"newest red, older green: newest decides", map[string]string{"runs.json": hRuns(push("11", "completed", "failure", 1), push("9", "completed", "success", 1)), "jobs-11.json": red, "jobs-9.json": green}, false, "test-go-cmd (macos-latest, shard 3)=failure", "11", "9"},
		{"newest green, older red: newest decides", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1), push("9", "completed", "failure", 1)), "jobs-11.json": green, "jobs-9.json": red}, true, "judged main run 11", "11", "9"},
		{"red Linux, green macOS passes", map[string]string{"runs.json": hRuns(push("11", "completed", "failure", 1)), "jobs-11.json": strings.Replace(green, `"success"`, `"failure"`, 1)}, true, "judged main run 11", "11", ""},
		{"scheduled run counts", map[string]string{"runs.json": hRuns(hRun{"11", "schedule", "completed", "success", 1}), "jobs-11.json": green}, true, "judged main run 11", "11", ""},
		{"cancelled macOS job", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": hJobs(map[string]string{"test-go-cmd (macos-latest, shard 1)": "cancelled"})}, false, "shard 1)=cancelled", "11", ""},
		{"timed out macOS job", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": hJobs(map[string]string{"test-go-cmd (macos-latest, shard 1)": "timed_out"})}, false, "=timed_out", "11", ""},
		{"skipped macOS job is red", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": hJobs(map[string]string{"test-go-cmd (macos-latest, shard 1)": "skipped"})}, false, "=skipped", "11", ""},
		{"all macOS jobs skipped is red", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": hJobs(map[string]string{healthExpected[0]: "skipped", healthExpected[1]: "skipped", healthExpected[2]: "skipped", healthExpected[3]: "skipped", healthExpected[4]: "skipped", healthExpected[5]: "skipped", healthExpected[6]: "skipped"})}, false, "=skipped", "11", ""},
		{"missing one expected job is red", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": hJobs(nil, healthExpected[6])}, false, "missing expected macOS jobs: test-go-cmd (macos-latest, shard 3)", "11", ""},
		{"no macOS jobs is red", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": hJob("lint", "success") + "\n"}, false, "missing expected macOS jobs", "11", ""},
		{"unexpected macOS job red", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": hJobs(nil) + hJob("extra (macos-14)", "failure") + "\n"}, false, "extra (macos-14)=failure", "11", ""},
		{"first-attempt run in progress is ignored", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 1), push("11", "completed", "success", 1)), "jobs-11.json": green, "jobs-12.json": red}, true, "judged main run 11", "11", "12"},
		{"queued first attempt is ignored", map[string]string{"runs.json": hRuns(push("12", "queued", "", 1), push("11", "completed", "success", 1)), "jobs-11.json": green}, true, "judged main run 11", "11", "12"},
		{"re-run in progress over red attempt fails closed", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 2), push("11", "completed", "success", 1)), "jobs-12-a1.json": red, "jobs-11.json": green}, false, "test-go-cmd (macos-latest, shard 3)=failure", "12", "11"},
		{"re-run in progress over green attempt is judged by it", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 2)), "jobs-12-a1.json": green}, true, "judging attempt 1", "12", ""},
		{"cancelled run with failed macOS job is red", map[string]string{"runs.json": hRuns(push("12", "completed", "cancelled", 1), push("11", "completed", "success", 1)), "jobs-12.json": red, "jobs-11.json": green}, false, "cancelled run 12", "12", "11"},
		{"cancelled run without failure is skipped", map[string]string{"runs.json": hRuns(push("12", "completed", "cancelled", 1), push("11", "completed", "success", 1)), "jobs-12.json": hJobs(map[string]string{healthExpected[0]: "cancelled"}), "jobs-11.json": green}, true, "judged main run 11", "11", ""},
		{"three cancelled runs then green passes", map[string]string{"runs.json": hRuns(push("14", "completed", "cancelled", 1), push("13", "completed", "cancelled", 1), push("12", "completed", "cancelled", 1), push("11", "completed", "success", 1)), "jobs-14.json": green, "jobs-13.json": green, "jobs-12.json": green, "jobs-11.json": green}, true, "judged main run 11", "11", ""},
		{"four cancelled runs fail closed", map[string]string{"runs.json": hRuns(push("15", "completed", "cancelled", 1), push("14", "completed", "cancelled", 1), push("13", "completed", "cancelled", 1), push("12", "completed", "cancelled", 1), push("11", "completed", "success", 1)), "jobs-15.json": green, "jobs-14.json": green, "jobs-13.json": green, "jobs-12.json": green, "jobs-11.json": green}, false, "consecutive cancelled", "12", "11"},
		{"pull_request and dispatch runs never count", map[string]string{"runs.json": hRuns(hRun{"13", "pull_request", "completed", "failure", 1}, hRun{"12", "workflow_dispatch", "completed", "failure", 1}, push("11", "completed", "success", 1)), "jobs-11.json": green}, true, "judged main run 11", "11", "13"},
		{"partial first page: red job on a later page is seen", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": red}, false, "=failure", "11", ""},
		{"no runs fails closed", map[string]string{"runs.json": `{"workflow_runs":[]}`}, false, "no completed main CI run", "", ""},
		{"only in-progress runs fails closed", map[string]string{"runs.json": hRuns(push("12", "in_progress", "", 1))}, false, "no completed main CI run", "", "12"},
		{"runs api error fails closed", map[string]string{"runs.err": "HTTP 502 Bad Gateway"}, false, "HTTP 502 Bad Gateway", "", ""},
		{"jobs api error fails closed", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.err": "HTTP 403 rate limit"}, false, "HTTP 403 rate limit", "11", ""},
		{"jobs api error after a partial page fails closed", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": hJobs(nil), "jobs-11.err": "HTTP 502 mid-pagination"}, false, "HTTP 502 mid-pagination", "11", ""},
		{"garbage runs fails closed", map[string]string{"runs.json": `not json`}, false, "cannot read main CI state", "", ""},
		{"empty runs fails closed", map[string]string{"runs.json": ``}, false, "cannot read main CI state", "", ""},
		{"null workflow_runs fails closed", map[string]string{"runs.json": `{"workflow_runs":null}`}, false, "cannot read main CI state", "", ""},
		{"empty jobs fails closed", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": ``}, false, "cannot read jobs", "11", ""},
		{"garbage jobs fails closed", map[string]string{"runs.json": hRuns(push("11", "completed", "success", 1)), "jobs-11.json": "not json\n"}, false, "cannot read jobs", "11", ""},
	}
	for _, tt := range tests {
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
		r := runHealth(t, map[string]string{"runs.json": hRuns(hRun{"11", "push", "completed", "success", 1}), "jobs-11.json": hJobs(nil)}, `[]`, args...)
		require.Error(t, r.err, r.out)
		require.Contains(t, r.out, "takes no arguments")
		require.Empty(t, r.requests)
	}
}

func TestMainMacOSHealth_Repository_UnsetFailsClosed(t *testing.T) {
	cmd := exec.Command("bash", "main-macos-health.sh")
	cmd.Env = append(os.Environ(), "GITHUB_REPOSITORY=", "PR_LABELS=[]")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))
	require.Contains(t, string(out), "GITHUB_REPOSITORY is unset")
}
