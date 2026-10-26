// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-203: the remote bar can run the Go bar on a macOS runner (--os) and
// can add the web npm checks with a dist byte compare (--web). The flags are
// validated against an allowlist, default runs dispatch exactly the inputs
// the workflow on main already knows, and the workflow pins both options.
package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const ciWorkflowPath = "../.github/workflows/ci.yml"

func dryRun(t *testing.T, args string) (string, int) {
	t.Helper()
	work, _ := repoWithRemote(t)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b"), 0o600))
	return bash(t, work, `bash "$RB_SCRIPT" --dry-run `+args+` "$S"`, "S="+snap)
}

func TestRemoteBar_OSFlag_RefusesAnythingOutsideTheAllowlistWithExit2(t *testing.T) {
	for _, bad := range []string{"windows-latest", "ubuntu-latest", "macos-14", "MACOS-LATEST", "ubuntu-22.04\\ ", "'a b'", "''", "self-hosted", "'$(id)'"} {
		t.Run(bad, func(t *testing.T) {
			out, code := dryRun(t, "--os "+bad)
			require.Equal(t, 2, code, out)
			require.Contains(t, out, "--os")
			require.NotContains(t, out, "nothing pushed")
		})
	}
	work, _ := repoWithRemote(t)
	out, code := bash(t, work, `bash "$RB_SCRIPT" --dry-run --os`)
	require.Equal(t, 2, code, out)
	require.Contains(t, out, "--os needs a value")
}

func TestRemoteBar_OSFlag_AcceptsTheTwoRunners(t *testing.T) {
	for _, tc := range []struct{ args, want string }{
		{"", "os=ubuntu-22.04 web=false"},
		{"--os ubuntu-22.04", "os=ubuntu-22.04 web=false"},
		{"--os macos-latest", "os=macos-latest web=false"},
		{"--web", "os=ubuntu-22.04 web=true"},
		{"--os macos-latest --web", "os=macos-latest web=true"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			out, code := dryRun(t, tc.args)
			require.Equal(t, 0, code, out)
			require.Contains(t, out, tc.want)
			require.Contains(t, out, "nothing pushed")
		})
	}
}

func dispatchLog(t *testing.T, args string) string {
	t.Helper()
	work, _ := repoWithRemote(t)
	bin, logPath := stubGh(t)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b"), 0o600))
	snippet := `source "$RB_SCRIPT"; rb_remote_urls_ok() { return 0; }; rb_install_traps; rb_run --ticket MTIX-203 ` + args + ` "$S"`
	out, code := bash(t, work, snippet, "S="+snap, "RB_REMOTE=origin", "STUB_LOG="+logPath, "RB_POLL_SECS=1",
		"PATH="+bin+":"+os.Getenv("PATH"))
	require.Equal(t, 0, code, out)
	logged, err := os.ReadFile(logPath) //nolint:gosec // test-owned path
	require.NoError(t, err)
	for _, l := range strings.Split(string(logged), "\n") {
		if strings.HasPrefix(l, "workflow run") {
			return l
		}
	}
	t.Fatalf("no dispatch in log: %s", logged)
	return ""
}

func TestRemoteBar_Dispatch_PassesOSAndWebOnlyWhenRequested(t *testing.T) {
	// A default run sends no new inputs, so it still dispatches against a
	// workflow on main that predates them (pre-merge and any older main).
	def := dispatchLog(t, "")
	require.NotContains(t, def, "-f os=")
	require.NotContains(t, def, "-f web=")
	require.NotContains(t, dispatchLog(t, "--os ubuntu-22.04"), "-f os=")

	mac := dispatchLog(t, "--os macos-latest")
	require.Contains(t, mac, "-f os=macos-latest")
	require.NotContains(t, mac, "-f web=")

	web := dispatchLog(t, "--web")
	require.Contains(t, web, "-f web=true")
	require.NotContains(t, web, "-f os=")

	both := dispatchLog(t, "--web --os macos-latest")
	require.Contains(t, both, "-f web=true")
	require.Contains(t, both, "-f os=macos-latest")
	for _, l := range []string{def, mac, web, both} {
		require.Contains(t, l, "-f scratch_ref=ci-scratch/MTIX-203-")
		require.Contains(t, l, "-f expected_tree=")
	}
}

func TestRemoteBar_Classify_WebChecksFailureIsABarFailure(t *testing.T) {
	for steps, want := range map[string]string{
		"Run web checks":                      "1",
		"Verify snapshot tree|Run web checks": "3",
		"Run committed bar|Run web checks":    "1",
		"Install web dependencies":            "2",
	} {
		out, code := bash(t, t.TempDir(), `source "$RB_SCRIPT"; rb_classify failure "$B"`, "B="+steps)
		require.Equal(t, 0, code, out)
		require.Equal(t, want, strings.TrimSpace(out), steps)
	}
}

// jobBlock returns the text of one top-level job of the workflow.
func jobBlock(t *testing.T, wf, job string) string {
	t.Helper()
	m := regexp.MustCompile(`(?ms)^  ` + regexp.QuoteMeta(job) + `:\n(.*?)(?:^  [a-z][a-z0-9-]*:\n|\z)`).FindStringSubmatch(wf)
	require.NotNil(t, m, "job %s not found", job)
	return m[1]
}

func TestRemoteBarWorkflow_Inputs_PinOSChoiceAndWebFlag(t *testing.T) {
	wf := readWorkflow(t)
	require.Regexp(t, `(?s)      os:\n.*?type: choice\n\s+default: ubuntu-22.04\n\s+options:\n\s+- ubuntu-22.04\n\s+- macos-latest\n`, wf)
	require.Regexp(t, `(?s)      web:\n.*?type: boolean\n\s+default: false\n`, wf)
	// The script allowlist and the workflow options are the same set.
	src, err := os.ReadFile(scriptPath)
	require.NoError(t, err)
	for _, o := range []string{"ubuntu-22.04", "macos-latest"} {
		require.Contains(t, string(src), o)
	}
}

func TestRemoteBarWorkflow_OSJobs_SplitByRunnerAndMacHasNoServices(t *testing.T) {
	wf := readWorkflow(t)
	linux := jobBlock(t, wf, "bar")
	require.Contains(t, linux, "if: ${{ inputs.os != 'macos-latest' }}")
	require.Contains(t, linux, "runs-on: ubuntu-22.04")
	require.Contains(t, linux, "postgres:16")

	mac := jobBlock(t, wf, "bar-macos")
	require.Contains(t, mac, "if: ${{ inputs.os == 'macos-latest' }}")
	require.Contains(t, mac, "runs-on: macos-latest")
	require.NotContains(t, mac, "services:", "service containers are Linux only")
	require.NotContains(t, mac, "MTIX_PG_TEST_DSN")
	// The mac job keeps the safety properties and the classified step names.
	for _, want := range []string{
		"- name: Verify snapshot tree", "persist-credentials: false", "fetch-depth: 0",
		"ref: ${{ inputs.scratch_ref }}", "run: make lint-go && make test-race",
		"- name: Run govulncheck", "run: go env -w GOFLAGS=-p=1",
	} {
		require.Contains(t, mac, want)
	}
	require.Less(t, strings.Index(mac, "Verify snapshot tree"), strings.Index(mac, "Run committed bar"))
	require.Less(t, strings.Index(mac, "Run committed bar"), strings.Index(mac, "Run govulncheck"))
}

func TestRemoteBarWorkflow_WebJob_RunsTheChecksInOrderAndComparesDist(t *testing.T) {
	wf := readWorkflow(t)
	web := jobBlock(t, wf, "web")
	require.Contains(t, web, "if: ${{ inputs.web }}")
	require.Contains(t, web, "runs-on: ubuntu-22.04")
	require.Contains(t, web, "persist-credentials: false")
	require.Contains(t, web, "ref: ${{ inputs.scratch_ref }}")
	require.Less(t, strings.Index(web, "Verify snapshot tree"), strings.Index(web, "Run web checks"))

	run := stepRun(t, web, "Run web checks")
	order := []string{"npm ci", "npm audit", "npm run lint", "npx tsc -b", "npx vitest run", "npm run build", "diff -r web/dist internal/web/dist"}
	last := -1
	for _, c := range order {
		i := strings.Index(run, c)
		require.Greater(t, i, last, "%q must follow the previous command", c)
		last = i
	}
	require.Contains(t, run, "set -euo pipefail")
	require.NotContains(t, run, "--omit=dev", "dev dependencies are audited too")
	require.NotContains(t, run, "--audit-level", "any advisory fails")
	require.NotContains(t, run, "|| true")

	// Same Node major as CI and release.
	node := regexp.MustCompile(`node-version: "(\d+)"`)
	ci, err := os.ReadFile(ciWorkflowPath)
	require.NoError(t, err)
	want := node.FindStringSubmatch(string(ci))
	got := node.FindStringSubmatch(web)
	require.NotNil(t, want)
	require.NotNil(t, got)
	require.Equal(t, want[1], got[1])
}

// fakeWebTree builds a checkout with stub npm/npx so the web step can run
// offline: npm run build copies fixtures/dist to web/dist.
func fakeWebTree(t *testing.T, committed, built string) (ws, bin string) {
	t.Helper()
	ws = t.TempDir()
	for dir, content := range map[string]string{"internal/web/dist": committed, "web/fresh": built} {
		require.NoError(t, os.MkdirAll(filepath.Join(ws, dir), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(ws, dir, "index.js"), []byte(content), 0o600))
	}
	bin = t.TempDir()
	npm := `#!/bin/sh
echo "npm $*" >> "$STUB_LOG"
[ "$1" = audit ] && [ -n "$STUB_AUDIT_FAIL" ] && exit 1
[ "$1" = run ] && [ "$2" = build ] && { rm -rf dist; cp -r fresh dist; }
exit 0
`
	npx := "#!/bin/sh\necho \"npx $*\" >> \"$STUB_LOG\"\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "npm"), []byte(npm), 0o700)) //nolint:gosec // test stub
	require.NoError(t, os.WriteFile(filepath.Join(bin, "npx"), []byte(npx), 0o700)) //nolint:gosec // test stub
	return ws, bin
}

func TestRemoteBarWorkflow_WebChecks_FailOnDistDifferenceOrAuditFinding(t *testing.T) {
	run := stepRun(t, jobBlock(t, readWorkflow(t), "web"), "Run web checks")
	cases := []struct {
		name, committed, built, auditFail string
		wantFail                          bool
	}{
		{"identical bundle passes", "x", "x", "", false},
		{"planted dist difference fails", "x", "y", "", true},
		{"audit finding fails", "x", "x", "1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, bin := fakeWebTree(t, tc.committed, tc.built)
			logPath := filepath.Join(t.TempDir(), "calls.log")
			out, code := bash(t, ws, run, "GITHUB_WORKSPACE="+ws, "STUB_LOG="+logPath,
				"STUB_AUDIT_FAIL="+tc.auditFail, "PATH="+bin+":"+os.Getenv("PATH"))
			if tc.wantFail {
				require.NotEqual(t, 0, code, out)
			} else {
				require.Equal(t, 0, code, out)
			}
			logged, err := os.ReadFile(logPath) //nolint:gosec // test-owned path
			require.NoError(t, err)
			if tc.auditFail != "" {
				require.NotContains(t, string(logged), "npm run lint", "stops at the failed audit")
			}
		})
	}
}

// MTIX-112.17.24: the Linux bar runs cmd/mtix as three PostgreSQL-backed
// shards next to the core packages.
func TestRemoteBarWorkflow_CmdShards_CoverCmdMtixWithUnchangedFlags(t *testing.T) {
	wf := readWorkflow(t)
	core := jobBlock(t, wf, "bar")
	require.Contains(t, core, "run: make lint-go && make test-race-core")
	require.NotContains(t, core, "make test-race\n")

	cmd := jobBlock(t, wf, "bar-cmd")
	require.Contains(t, cmd, "if: ${{ inputs.os != 'macos-latest' }}")
	require.Contains(t, cmd, `shard: ["1", "2", "3"]`)
	require.Contains(t, cmd, "fail-fast: false")
	require.Contains(t, cmd, "postgres:16")
	require.Contains(t, cmd, "persist-credentials: false")
	require.Contains(t, cmd, "- name: Verify snapshot tree")
	require.Contains(t, cmd, "- name: Verify exact cmd/mtix test coverage")
	require.Contains(t, cmd, "- name: Run cmd/mtix shard")
	run := stepRun(t, cmd, "Run cmd/mtix shard")
	require.Contains(t, run, `bash scripts/cmd-mtix-shards.sh pattern "$SHARD"`)
	require.Contains(t, run, `go test ./cmd/mtix/ -race -count=1 -p 1 -timeout 20m -run "$pattern"`)
	require.NotContains(t, run, "-skip")
	require.Less(t, strings.Index(cmd, "Verify snapshot tree"), strings.Index(cmd, "Run cmd/mtix shard"))
	// The macOS job still runs the whole package set.
	require.Contains(t, jobBlock(t, wf, "bar-macos"), "run: make lint-go && make test-race\n")
}

func TestRemoteBar_Classify_CmdShardFailureIsABarFailure(t *testing.T) {
	for steps, want := range map[string]string{
		"Run cmd/mtix shard":                      "1",
		"Verify exact cmd/mtix test coverage":     "1",
		"Verify snapshot tree|Run cmd/mtix shard": "3",
		"Run committed bar|Run cmd/mtix shard":    "1",
		"Run cmd/mtix":                            "2",
	} {
		out, code := bash(t, t.TempDir(), `source "$RB_SCRIPT"; rb_classify failure "$B"`, "B="+steps)
		require.Equal(t, 0, code, out)
		require.Equal(t, want, strings.TrimSpace(out), steps)
	}
}
