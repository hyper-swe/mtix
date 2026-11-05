// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.17.42: scripts/main-macos-health.sh blocks pull_request runs while
// the latest completed main CI run (push or nightly schedule) has a failed
// macOS job. Policy under test: fail closed on any API error or unreadable or
// unknown state; a newer in-progress main run is ignored (neither green nor
// blocking); a wholly cancelled main run says nothing and is skipped; runs of
// other events never count; the maintainer-applied label main-macos-fix lets
// the PR that repairs main merge.
package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const healthRunsGreen = `{"workflow_runs":[{"id":11,"event":"push","status":"completed","conclusion":"success"}]}`

func healthJobs(macos ...string) string {
	jobs := []string{`{"name":"test-go-cmd (ubuntu-22.04, shard 1)","conclusion":"success"}`}
	for _, c := range macos {
		jobs = append(jobs, `{"name":"test-go-cmd (macos-latest, shard 1)","conclusion":"`+c+`"}`)
	}
	return strings.Join(jobs, "\n")
}

func runHealth(t *testing.T, runs, jobs, labels string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	rf, jf := filepath.Join(dir, "runs.json"), filepath.Join(dir, "jobs.json")
	require.NoError(t, os.WriteFile(rf, []byte(runs), 0o600))
	require.NoError(t, os.WriteFile(jf, []byte(jobs), 0o600))
	cmd := exec.Command("bash", "main-macos-health.sh", "--runs", rf, "--jobs", jf)
	cmd.Env = append(os.Environ(), "PR_LABELS="+labels)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestMainMacOSHealth_Decisions_OverFixtures(t *testing.T) {
	inProgressNewer := `{"workflow_runs":[{"id":12,"event":"push","status":"in_progress","conclusion":null},` +
		`{"id":11,"event":"push","status":"completed","conclusion":"success"}]}`
	cancelledNewer := `{"workflow_runs":[{"id":12,"event":"push","status":"completed","conclusion":"cancelled"},` +
		`{"id":11,"event":"schedule","status":"completed","conclusion":"success"}]}`
	prRunRed := `{"workflow_runs":[{"id":13,"event":"pull_request","status":"completed","conclusion":"failure"},` +
		`{"id":11,"event":"push","status":"completed","conclusion":"success"}]}`
	onlyPR := `{"workflow_runs":[{"id":13,"event":"pull_request","status":"completed","conclusion":"success"}]}`
	onlyCancelled := `{"workflow_runs":[{"id":12,"event":"push","status":"completed","conclusion":"cancelled"}]}`
	tests := []struct {
		name, runs, jobs, labels string
		wantPass                 bool
		wantText                 string
	}{
		{"green main", healthRunsGreen, healthJobs("success", "success"), `[]`, true, ""},
		{"red macos job", healthRunsGreen, healthJobs("success", "failure"), `[]`, false, "macOS"},
		{"cancelled macos job", healthRunsGreen, healthJobs("cancelled"), `[]`, false, "macOS"},
		{"timed out macos job", healthRunsGreen, healthJobs("timed_out"), `[]`, false, "macOS"},
		{"unknown macos conclusion", healthRunsGreen, healthJobs(""), `[]`, false, "macOS"},
		{"no macos jobs in run fails closed", healthRunsGreen, healthJobs(), `[]`, false, "no macOS"},
		{"no completed run fails closed", `{"workflow_runs":[]}`, healthJobs("success"), `[]`, false, "no completed"},
		{"only pull_request runs fails closed", onlyPR, healthJobs("success"), `[]`, false, "no completed"},
		{"only cancelled runs fails closed", onlyCancelled, healthJobs("success"), `[]`, false, "no completed"},
		{"garbage runs json fails closed", `not json`, healthJobs("success"), `[]`, false, "cannot read"},
		{"empty runs fails closed", ``, healthJobs("success"), `[]`, false, "cannot read"},
		{"garbage jobs fails closed", healthRunsGreen, `not json`, `[]`, false, "cannot read"},
		{"in-progress newer run is ignored, older green passes", inProgressNewer, healthJobs("success"), `[]`, true, "in progress"},
		{"cancelled newer run is skipped, older green passes", cancelledNewer, healthJobs("success"), `[]`, true, ""},
		{"pull_request run never counts", prRunRed, healthJobs("success"), `[]`, true, ""},
		{"label override passes red main", healthRunsGreen, healthJobs("failure"), `["main-macos-fix"]`, true, "override"},
		{"label override passes even with unreadable state", ``, ``, `["main-macos-fix"]`, true, "override"},
		{"other labels do not override", healthRunsGreen, healthJobs("failure"), `["bug"]`, false, "macOS"},
		{"unparsable labels do not override", healthRunsGreen, healthJobs("failure"), `oops`, false, "macOS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runHealth(t, tt.runs, tt.jobs, tt.labels)
			if tt.wantPass {
				require.NoError(t, err, out)
			} else {
				require.Error(t, err, out)
			}
			require.Contains(t, out, tt.wantText)
		})
	}
}

func TestMainMacOSHealth_LiveMode_FailsClosedOnAPIError(t *testing.T) {
	bin := t.TempDir()
	stub := "#!/bin/bash\necho 'HTTP 502' >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(stub), 0o700))
	cmd := exec.Command("bash", "main-macos-health.sh")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"),
		"GITHUB_REPOSITORY=example/repo", "PR_LABELS=[]")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))
	require.Contains(t, string(out), "cannot read")
}
