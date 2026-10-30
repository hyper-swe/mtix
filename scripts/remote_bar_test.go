// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-167: the remote full-bar wrapper only ever pushes ci-scratch/* to the
// public repository, names branches predictably, classifies run results into
// distinct exit codes, deletes its branch on every exit path, and the
// workflow refuses a checkout whose tree differs from the snapshot.
package scripts_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	scriptPath   = "remote-bar.sh"
	workflowPath = "../.github/workflows/remote-bar.yml"
)

// cleanEnv isolates git from host configuration.
func cleanEnv(extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "TMPDIR=" + os.TempDir(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@invalid",
	}
	return append(env, extra...)
}

// bash runs a snippet in dir and returns combined output and the exit code.
func bash(t *testing.T, dir, snippet string, extra ...string) (string, int) {
	t.Helper()
	abs, err := filepath.Abs(scriptPath)
	require.NoError(t, err)
	cmd := exec.Command("bash", "-c", snippet)
	cmd.Dir = dir
	cmd.Env = cleanEnv(append([]string{"RB_SCRIPT=" + abs}, extra...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.String(), exitErr.ExitCode()
	}
	require.NoError(t, err, out.String())
	return out.String(), 0
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

// repoWithRemote builds a work repo whose origin is a local bare repo.
func repoWithRemote(t *testing.T) (work, bare string) {
	t.Helper()
	root := t.TempDir()
	bare = filepath.Join(root, "bare.git")
	work = filepath.Join(root, "work")
	require.NoError(t, os.Mkdir(work, 0o750))
	git(t, root, "init", "--bare", "-b", "main", bare)
	git(t, work, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o600))
	git(t, work, "add", "-A")
	git(t, work, "commit", "-m", "base")
	git(t, work, "remote", "add", "origin", bare)
	git(t, work, "push", "origin", "main")
	git(t, work, "fetch", "origin")
	return work, bare
}

func TestRemoteBar_RemoteOk_AcceptsOnlyPublicRepo(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://github.com/hyper-swe/mtix.git", true},
		{"https://github.com/hyper-swe/mtix", true},
		{"git@github.com:hyper-swe/mtix.git", true},
		{"ssh://git@github.com/hyper-swe/mtix.git", true},
		{"https://github.com/hyper-swe/mtix-dev.git", false},
		{"https://github.com/other/mtix.git", false},
		{"https://evil.example/hyper-swe/mtix.git", false},
		{"https://github.com/hyper-swe/mtix.git/extra", false},
		{"/some/local/path", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			out, code := bash(t, t.TempDir(), `source "$RB_SCRIPT"; rb_remote_ok '`+tc.url+`'`)
			require.Equal(t, tc.want, code == 0, out)
		})
	}
}

func TestRemoteBar_BranchName_FollowsConvention(t *testing.T) {
	cases := []struct {
		name, ticket, stamp, tree, want string
		ok                              bool
	}{
		{"plain", "MTIX-167", "20261014T061500Z", "abcdef0123456789", "ci-scratch/MTIX-167-20261014T061500Z-abcdef01", true},
		{"dots sanitized", "MTIX-112.17.3", "20261014T061500Z", "abcdef0123456789", "ci-scratch/MTIX-112-17-3-20261014T061500Z-abcdef01", true},
		{"hostile ticket", "a b;c/../x", "20261014T061500Z", "abcdef0123456789", "ci-scratch/a-b-c----x-20261014T061500Z-abcdef01", true},
		{"short tree", "T", "20261014T061500Z", "abc", "", false},
		{"empty ticket", "", "20261014T061500Z", "abcdef0123456789", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := bash(t, t.TempDir(),
				`source "$RB_SCRIPT"; rb_branch_name "$A" "$B" "$C"`, "A="+tc.ticket, "B="+tc.stamp, "C="+tc.tree)
			require.Equal(t, tc.ok, code == 0, out)
			if tc.ok {
				require.Equal(t, tc.want, strings.TrimSpace(out))
			}
		})
	}
}

func TestRemoteBar_AssertRef_RefusesEverythingButScratchBranches(t *testing.T) {
	cases := []struct {
		ref  string
		want bool
	}{
		{"refs/heads/ci-scratch/MTIX-167-20261014T061500Z-abcdef01", true},
		{"refs/heads/main", false},
		{"refs/heads/ci-scratch/", false},
		{"refs/heads/ci-scratch", false},
		{"refs/tags/ci-scratch/x", false},
		{"ci-scratch/x", false},
		{"+refs/heads/ci-scratch/x", false},
		{"refs/heads/ci-scratch/../main", false},
		{"refs/heads/ci-scratch/x:refs/heads/main", false},
		{"refs/heads/ci-scratch/x y", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			out, code := bash(t, t.TempDir(), `source "$RB_SCRIPT"; rb_assert_ref "$R"`, "R="+tc.ref)
			require.Equal(t, tc.want, code == 0, out)
		})
	}
}

func TestRemoteBar_Push_RefusesNonScratchTargetAndPushesNothing(t *testing.T) {
	work, bare := repoWithRemote(t)
	head := git(t, work, "rev-parse", "HEAD")
	for _, branch := range []string{"main", "release/0.5", "ci-scratch", "feature/x"} {
		out, code := bash(t, work, `source "$RB_SCRIPT"; rb_push "$C" "$B"`,
			"RB_REMOTE=origin", "C="+head, "B="+branch)
		require.Equal(t, 2, code, out)
		require.Contains(t, out, "refusing")
	}
	require.Equal(t, "refs/heads/main", strings.Fields(git(t, bare, "for-each-ref", "--format=%(refname)"))[0])
	require.Len(t, strings.Fields(git(t, bare, "for-each-ref", "--format=%(refname)")), 1)
}

func TestRemoteBar_DryRun_SnapshotsWithoutTouchingCallerState(t *testing.T) {
	work, _ := repoWithRemote(t)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, ".gitignore"), []byte("ignored.txt\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, "ignored.txt"), []byte("i"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(snap, ".mgit"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(snap, ".mgit", "y"), []byte("y"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(snap, ".hyperswe"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(snap, ".hyperswe", "x"), []byte("x"), 0o600))
	headBefore := git(t, work, "rev-parse", "HEAD")

	// Independent computation of the expected tree in a separate repo.
	ref := t.TempDir()
	git(t, ref, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(ref, ".gitignore"), []byte("ignored.txt\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(ref, "b.txt"), []byte("b\n"), 0o600))
	git(t, ref, "add", "-A")
	want := git(t, ref, "write-tree")

	out, code := bash(t, work, `bash "$RB_SCRIPT" --dry-run --ticket MTIX-167 "$S"`, "S="+snap)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "tree="+want)
	require.Contains(t, out, "branch=ci-scratch/MTIX-167-")
	require.Contains(t, out, want[:8])
	require.Contains(t, out, "nothing pushed")
	require.Equal(t, headBefore, git(t, work, "rev-parse", "HEAD"))
	require.Empty(t, git(t, work, "diff", "--cached", "--name-only"))
	require.Empty(t, git(t, work, "status", "--porcelain"))
	require.Empty(t, git(t, work, "branch", "--list", "ci-scratch/*"))
}

func TestRemoteBar_Cleanup_DeletesBranchOnExitFailureAndSignal(t *testing.T) {
	cases := []struct {
		name, tail string
		wantCode   int
	}{
		{"normal exit", "exit 0", 0},
		{"failure exit keeps its status", "exit 5", 5},
		{"term signal", `kill -TERM $$; sleep 5`, 143},
		{"interrupt signal", `kill -INT $$; sleep 5`, 130},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work, bare := repoWithRemote(t)
			head := git(t, work, "rev-parse", "HEAD")
			snippet := `source "$RB_SCRIPT"; rb_install_traps
RB_TMPDIR="$(mktemp -d "$TMPDIR/rbtest.XXXXXX")"; echo "$RB_TMPDIR" > "$OUT"
RB_BRANCH=ci-scratch/MTIX-1-20261014T061500Z-abcdef01
rb_push "$C" "$RB_BRANCH"
` + tc.tail
			tmpRec := filepath.Join(t.TempDir(), "tmpdir")
			out, code := bash(t, work, snippet, "RB_REMOTE=origin", "C="+head, "OUT="+tmpRec)
			require.Equal(t, tc.wantCode, code, out)
			require.Empty(t, git(t, bare, "for-each-ref", "refs/heads/ci-scratch"), "scratch branch must be gone")
			rec, err := os.ReadFile(tmpRec) //nolint:gosec // test-owned path
			require.NoError(t, err)
			_, statErr := os.Stat(strings.TrimSpace(string(rec)))
			require.True(t, os.IsNotExist(statErr), "temp dir must be removed")
		})
	}
}

func TestRemoteBar_Classify_MapsRunResultsToDistinctExitCodes(t *testing.T) {
	cases := []struct {
		conclusion, steps string
		want              string
	}{
		{"success", "", "0"},
		{"failure", "Run committed bar", "1"},
		{"failure", "Run govulncheck", "1"},
		{"failure", "Verify snapshot tree", "3"},
		{"failure", "Verify snapshot tree|Run committed bar", "3"},
		{"failure", "", "2"},
		{"failure", "Set up job", "2"},
		{"failure", "Run actions/checkout", "2"},
		{"cancelled", "", "2"},
		{"timed_out", "", "2"},
		{"", "", "2"},
	}
	for _, tc := range cases {
		t.Run(tc.conclusion+"/"+tc.steps, func(t *testing.T) {
			out, code := bash(t, t.TempDir(), `source "$RB_SCRIPT"; rb_classify "$A" "$B"`, "A="+tc.conclusion, "B="+tc.steps)
			require.Equal(t, 0, code, out)
			require.Equal(t, tc.want, strings.TrimSpace(out))
		})
	}
}

func readWorkflow(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	return string(b)
}

// stepRun returns the run block of the named workflow step.
func stepRun(t *testing.T, wf, name string) string {
	t.Helper()
	lines := strings.Split(wf, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "- name: "+name {
			continue
		}
		var body []string
		in := false
		for _, n := range lines[i+1:] {
			if !in {
				if strings.TrimSpace(n) == "run: |" {
					in = true
				}
				continue
			}
			if strings.TrimSpace(n) != "" && !strings.HasPrefix(n, "          ") {
				break
			}
			body = append(body, strings.TrimPrefix(n, "          "))
		}
		return strings.Join(body, "\n")
	}
	t.Fatalf("step %q not found", name)
	return ""
}

func TestRemoteBarWorkflow_VerifyStep_FailsOnTreeMismatch(t *testing.T) {
	run := stepRun(t, readWorkflow(t), "Verify snapshot tree")
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f"), []byte("f"), 0o600))
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "c")
	tree := git(t, repo, "rev-parse", "HEAD^{tree}")

	out, code := bash(t, repo, run, "EXPECTED_TREE="+tree)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "tree verified")

	out, code = bash(t, repo, run, "EXPECTED_TREE=0000000000000000000000000000000000000000")
	require.NotEqual(t, 0, code, out)
	require.Contains(t, out, "tree mismatch")

	_, code = bash(t, repo, run, "EXPECTED_TREE=")
	require.NotEqual(t, 0, code, "an empty expected tree must fail")
}

func TestRemoteBarWorkflow_Shape_IsHostedReadOnlyAndSecretFree(t *testing.T) {
	wf := readWorkflow(t)
	require.Contains(t, wf, "workflow_dispatch:")
	for _, forbidden := range []string{"pull_request", "push:", "self-hosted", "secrets."} {
		require.NotContains(t, wf, forbidden)
	}
	require.Contains(t, wf, "permissions:\n  contents: read")
	require.Contains(t, wf, "runs-on: ubuntu-22.04")
	require.NotContains(t, wf, "${{ runner.", "runner context is invalid in job-level env")
	require.Contains(t, wf, "persist-credentials: false")
	require.Contains(t, wf, "fetch-depth: 0")
	require.Contains(t, wf, "- name: Run govulncheck")
	require.Contains(t, wf, "ref: ${{ inputs.scratch_ref }}")
	require.Contains(t, wf, "run: make lint-go && make test-race")
	// The first step prints runner, run URL and wrapper version.
	first := stepRun(t, wf, "Identify run")
	for _, want := range []string{"$RUNNER_NAME", "$RUNNER_OS", "GITHUB_RUN_ID", "WRAPPER_VERSION"} {
		require.Contains(t, first, want)
	}
	require.Less(t, strings.Index(wf, "Identify run"), strings.Index(wf, "actions/checkout"))
	require.Less(t, strings.Index(wf, "actions/checkout"), strings.Index(wf, "Verify snapshot tree"))
	require.Less(t, strings.Index(wf, "Verify snapshot tree"), strings.Index(wf, "Run committed bar"))
	// Step names the wrapper classifies on must match the workflow.
	for _, name := range []string{"Verify snapshot tree", "Run committed bar", "Run govulncheck"} {
		require.Contains(t, wf, "- name: "+name)
		src, err := os.ReadFile(scriptPath)
		require.NoError(t, err)
		require.Contains(t, string(src), `"`+name+`"`)
	}
}

func TestRemoteBarWorkflow_Actionlint_IsClean(t *testing.T) {
	path, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed; workflow syntax and expression contexts not linted here (install it to run this check)")
	}
	cmd := exec.Command(path, workflowPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestRemoteBar_Push_IgnoresFollowTagsConfigAndPushesOnlyTheScratchRef(t *testing.T) {
	work, bare := repoWithRemote(t)
	head := git(t, work, "rev-parse", "HEAD")
	git(t, work, "config", "push.followTags", "true")
	git(t, work, "tag", "-a", "v9.9.9", "-m", "release", head)
	out, code := bash(t, work, `source "$RB_SCRIPT"; rb_push "$C" "$B"`,
		"RB_REMOTE=origin", "C="+head, "B=ci-scratch/MTIX-1-20261014T061500Z-abcdef01")
	require.Equal(t, 0, code, out)
	refs := git(t, bare, "for-each-ref", "--format=%(refname)")
	require.Contains(t, refs, "refs/heads/ci-scratch/MTIX-1-20261014T061500Z-abcdef01")
	require.NotContains(t, refs, "refs/tags/")
}

func TestRemoteBar_Push_IsNeverForced(t *testing.T) {
	work, bare := repoWithRemote(t)
	head := git(t, work, "rev-parse", "HEAD")
	tree := git(t, work, "rev-parse", "HEAD^{tree}")
	other := git(t, work, "commit-tree", tree, "-m", "unrelated")
	const branch = "ci-scratch/MTIX-1-20261014T061500Z-abcdef01"
	git(t, work, "push", "origin", other+":refs/heads/"+branch)
	out, code := bash(t, work, `source "$RB_SCRIPT"; rb_push "$C" "$B"`, "RB_REMOTE=origin", "C="+head, "B="+branch)
	require.NotEqual(t, 0, code, out)
	require.Equal(t, other, git(t, bare, "rev-parse", "refs/heads/"+branch), "existing ref must not be overwritten")
}

func TestRemoteBar_Run_RefusesForeignFetchOrPushURL(t *testing.T) {
	const public = "https://github.com/hyper-swe/mtix.git"
	cases := []struct {
		name, fetch, push, remote string
	}{
		{"local bare origin", "", "", ""},
		{"missing remote name", "", "", "nonexistent"},
		{"public fetch, foreign pushurl", public, "/tmp/elsewhere.git", ""},
		{"public fetch, other repo pushurl", public, "https://github.com/other/mtix.git", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work, bare := repoWithRemote(t)
			if tc.fetch != "" {
				git(t, work, "remote", "set-url", "origin", tc.fetch)
				git(t, work, "remote", "set-url", "--push", "origin", tc.push)
			}
			snap := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b"), 0o600))
			env := []string{"S=" + snap}
			if tc.remote != "" {
				env = append(env, "RB_REMOTE="+tc.remote)
			}
			out, code := bash(t, work, `bash "$RB_SCRIPT" "$S"`, env...)
			require.Equal(t, 2, code, out)
			require.Contains(t, out, "is not hyper-swe/mtix; refusing")
			require.Empty(t, git(t, bare, "for-each-ref", "refs/heads/ci-scratch"))
		})
	}
}

func TestRemoteBar_Run_RefusesWorkflowChangesUnlessAllowed(t *testing.T) {
	work, _ := repoWithRemote(t)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "a.txt"), []byte("a\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(snap, ".github", "workflows"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(snap, ".github", "workflows", "x.yml"), []byte("on: push\n"), 0o600))

	out, code := bash(t, work, `bash "$RB_SCRIPT" --dry-run "$S"`, "S="+snap)
	require.Equal(t, 2, code, out)
	require.Contains(t, out, "--allow-workflow-diff")

	out, code = bash(t, work, `bash "$RB_SCRIPT" --dry-run --allow-workflow-diff "$S"`, "S="+snap)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "WARNING")

	// An unchanged .github subtree is not a diff.
	require.NoError(t, os.RemoveAll(filepath.Join(snap, ".github")))
	_, code = bash(t, work, `bash "$RB_SCRIPT" --dry-run "$S"`, "S="+snap)
	require.Equal(t, 0, code)
}

func TestRemoteBar_Snapshot_KeepsTrackedFilesDespiteIgnoreRules(t *testing.T) {
	work, _ := repoWithRemote(t) // main tracks a.txt
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "a.txt"), []byte("a2\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, ".gitignore"), []byte("a.txt\n"), 0o600))
	globalIgnore := filepath.Join(t.TempDir(), "ignore")
	require.NoError(t, os.WriteFile(globalIgnore, []byte("b.txt\n"), 0o600))
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(cfg, []byte("[core]\n\texcludesFile = "+globalIgnore+"\n"), 0o600))

	out, code := bash(t, work, `bash "$RB_SCRIPT" --dry-run "$S"`, "S="+snap, "GIT_CONFIG_GLOBAL="+cfg)
	require.Equal(t, 0, code, out)
	var tree string
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "tree=") {
			tree = strings.TrimPrefix(f, "tree=")
		}
	}
	require.NotEmpty(t, tree, out)
	files := git(t, work, "ls-tree", "-r", "--name-only", tree)
	require.Contains(t, files, "a.txt", "tracked on main, so kept despite the ignore rule")
	require.Contains(t, files, "b.txt", "the caller's global excludes must not apply")
}

// stubGh writes a gh stub that records its arguments and answers with
// realistic JSON, applying the --jq expression it is given with jq.
func stubGh(t testing.TB) (binDir, logFile string) {
	t.Helper()
	requireJQ(t, exec.LookPath, os.Getenv)
	binDir = t.TempDir()
	logFile = filepath.Join(binDir, "gh.log")
	script := `#!/bin/sh
echo "$*" >> "$STUB_LOG"
D="$(dirname "$STUB_LOG")"
jqexpr=""
prev=""
for a in "$@"; do
  [ "$prev" = "--jq" ] && jqexpr="$a"
  case "$a" in scratch_ref=*) echo "${a#scratch_ref=}" > "$D/ref" ;; esac
  prev="$a"
done
emit() { if [ -n "$jqexpr" ]; then printf '%s' "$1" | jq -r "$jqexpr"; else printf '%s\n' "$1"; fi; }
case "$1 $2" in
"workflow run")
  [ -n "$STUB_FAIL_DISPATCH" ] && { echo "HTTP 502 bad gateway" >&2; exit 1; }
  case "$*" in *base_commit=*) [ -n "$STUB_REJECT_BASE" ] && { echo "Unexpected inputs provided: base_commit" >&2; exit 1; } ;; esac
  exit 0 ;;
"run list")
  case "$*" in *"--status success"*) emit "${STUB_REUSE_RUNS:-[]}"; exit 0 ;; esac
  n=$(cat "$D/lists" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$D/lists"
  if [ -n "$STUB_LIST_EMPTY_FIRST" ] && [ "$n" -eq 1 ]; then emit '[]'; exit 0; fi
  emit "[{\"databaseId\":1111,\"displayTitle\":\"remote-bar ci-scratch/other\"},{\"databaseId\":4242,\"displayTitle\":\"remote-bar $(cat "$D/ref")\"}]" ;;
"run cancel") exit 0 ;;
"api repos/hyper-swe/mtix/contents/.github/workflows/remote-bar.yml"*)
  case "$*" in
  *"ref=main"*) [ -n "$STUB_MAIN_BLOB" ] && emit "{\"sha\":\"$STUB_MAIN_BLOB\"}" ;;
  *) [ -n "$STUB_RUN_BLOB" ] && emit "{\"sha\":\"$STUB_RUN_BLOB\"}" ;;
  esac ;;
"run view")
  case "$*" in
  *"--json headBranch"*)
    [ -n "$STUB_FAIL_SOURCE" ] && exit 1
    emit "{\"headBranch\":\"${STUB_WORKFLOW_REF-main}\",\"headSha\":\"${STUB_WORKFLOW_SHA-1111111111111111111111111111111111111111}\"}" ;;
  *"--json status"*)
    st="${STUB_STATE:-completed success}"; status="${st%% *}"; concl="${st#* }"; [ "$concl" = "$st" ] && concl=""
    if [ -n "$concl" ]; then emit "{\"status\":\"$status\",\"conclusion\":\"$concl\"}"; else emit "{\"status\":\"$status\",\"conclusion\":null}"; fi ;;
  *"--json jobs"*)
    steps='{"name":"Set up job","conclusion":"success"}'
    old="$IFS"; IFS='|'; for n in $STUB_STEPS; do steps="$steps,{\"name\":\"$n\",\"conclusion\":\"failure\"}"; done; IFS="$old"
    emit "{\"jobs\":[{\"steps\":[$steps]}]}" ;;
  *"--log-failed"*) if [ -n "$STUB_FAILED_LOG" ]; then cat "$STUB_FAILED_LOG"; else echo "stub failed log"; fi ;;
  esac ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o700)) //nolint:gosec // test stub must be executable
	return binDir, logFile
}

func runWithStub(t *testing.T, env ...string) (out string, code int, bare, logPath string) {
	t.Helper()
	work, bare := repoWithRemote(t)
	bin, logPath := stubGh(t)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b"), 0o600))
	snippet := `source "$RB_SCRIPT"; rb_remote_urls_ok() { return 0; }; rb_install_traps; rb_run --ticket MTIX-167 "$S"`
	all := append([]string{"S=" + snap, "RB_REMOTE=origin", "STUB_LOG=" + logPath, "RB_POLL_SECS=1",
		"PATH=" + bin + ":" + os.Getenv("PATH")}, env...)
	out, code = bash(t, work, snippet, all...)
	return out, code, bare, logPath
}

func TestRemoteBar_Run_MapsRunOutcomesToExitCodesAndDeletesBranch(t *testing.T) {
	cases := []struct {
		name  string
		env   []string
		want  int
		extra string
	}{
		{"green", []string{"STUB_STATE=completed success"}, 0, "exit=0"},
		{"bar red", []string{"STUB_STATE=completed failure", "STUB_STEPS=Run committed bar"}, 1, "stub failed log"},
		{"tree mismatch", []string{"STUB_STATE=completed failure", "STUB_STEPS=Verify snapshot tree"}, 3, "exit=3"},
		{"infra", []string{"STUB_STATE=completed cancelled"}, 2, "exit=2"},
		{"run not listed on the first lookup", []string{"STUB_STATE=completed success", "STUB_LIST_EMPTY_FIRST=1", "RB_FIND_SECS=1"}, 0, "exit=0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code, bare, logPath := runWithStub(t, tc.env...)
			require.Equal(t, tc.want, code, out)
			require.Contains(t, out, " workflow_ref=main workflow_sha=")
			require.Contains(t, out, "workflow_sha="+strings.Repeat("1", 40))
			require.Contains(t, out, tc.extra)
			require.Empty(t, git(t, bare, "for-each-ref", "refs/heads/ci-scratch"), "scratch branch must be deleted")
			logged, err := os.ReadFile(logPath) //nolint:gosec // test-owned path
			require.NoError(t, err)
			require.Contains(t, string(logged), "workflow run remote-bar.yml --repo hyper-swe/mtix --ref main")
			require.Contains(t, string(logged), "-f expected_tree=")
			require.Contains(t, string(logged), "-f scratch_ref=ci-scratch/MTIX-167-")
			require.Contains(t, string(logged), "run list --repo hyper-swe/mtix --workflow remote-bar.yml --event workflow_dispatch")
			require.Contains(t, string(logged), "contains(\"ci-scratch/MTIX-167-")
			require.Contains(t, out, "actions/runs/4242", "the run is picked by title, not the decoy")
			require.NotContains(t, string(logged), "run cancel", "a finished run is not cancelled")
		})
	}
}

// TestRemoteBar_Run_FailedLogListsFailingTestsAheadOfTheTail pins MTIX-215: a
// failed run prints the failing-test lines of the whole log first (they can lie
// far outside the last 200 lines), deduplicated and capped, then the tail; the
// verdict line and exit code are unchanged and the captured log is cleaned up.
func TestRemoteBar_Run_FailedLogListsFailingTestsAheadOfTheTail(t *testing.T) {
	var log strings.Builder
	log.WriteString("bar\trun\t2026-10-30T01:00:00.0000000Z --- FAIL: TestEarly_One (0.01s)\n")
	log.WriteString("bar\trun\t2026-10-30T01:00:01.0000000Z --- FAIL: TestEarly_One (0.02s)\n")
	log.WriteString("bar\trun\t2026-10-30T01:00:02.0000000Z panic: boom [recovered]\n")
	log.WriteString("bar\trun\t2026-10-30T01:00:03.0000000Z FAIL\tgithub.com/hyper-swe/mtix/internal/early\t0.5s\n")
	log.WriteString("bar\trun\t2026-10-30T01:00:04.0000000Z FAIL github.com/hyper-swe/mtix/internal/broken [build failed]\n")
	log.WriteString("bar\trun\t2026-10-30T01:00:05.0000000Z panic: test timed out after 20m0s\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&log, "bar\trun\t2026-10-30T01:01:00.0000000Z filler line %d\n", i)
	}
	logFile := filepath.Join(t.TempDir(), "failed.log")
	require.NoError(t, os.WriteFile(logFile, []byte(log.String()), 0o600))

	out, code, _, _ := runWithStub(t, "STUB_STATE=completed failure", "STUB_STEPS=Run committed bar", "STUB_FAILED_LOG="+logFile)

	require.Equal(t, 1, code, out)
	require.Contains(t, out, " workflow_ref=main workflow_sha=")
	header := strings.Index(out, "failing tests:")
	require.GreaterOrEqual(t, header, 0, "a failing tests block is printed:\n%s", out)
	tail := strings.Index(out, "filler line 100")
	require.Greater(t, tail, header, "the tail follows the failing tests block")
	block := out[header:tail]
	for _, want := range []string{"--- FAIL: TestEarly_One", "panic: boom", "FAIL\tgithub.com/hyper-swe/mtix/internal/early", "[build failed]", "test timed out"} {
		require.Contains(t, block, want)
	}
	require.Equal(t, 1, strings.Count(block, "--- FAIL: TestEarly_One"), "duplicates collapse")
	require.NotContains(t, out, "filler line 99\n", "only the last 200 lines are tailed")
	require.Contains(t, out, "filler line 299")
}

// failedLogFile writes lines as a fake gh failed-run log and returns its path.
func failedLogFile(t *testing.T, lines []string) string {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "failed.log")
	require.NoError(t, os.WriteFile(logFile, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return logFile
}

// failingBlock returns the lines printed between the failing tests header and
// the log tail header, failing the test when either header is missing or
// misordered.
func failingBlock(t *testing.T, out string) []string {
	t.Helper()
	header := strings.Index(out, "failing tests:\n")
	tail := strings.Index(out, "log tail (last 200 lines):\n")
	require.GreaterOrEqual(t, header, 0, "failing tests header:\n%s", out)
	require.Greater(t, tail, header, "log tail header follows the failing tests block:\n%s", out)
	return strings.Split(strings.TrimSpace(out[header+len("failing tests:\n"):tail]), "\n")
}

// TestRemoteBar_Run_FailingTestsBlockIsCappedAtOneHundredLines pins the cap:
// 150 distinct failures print exactly the first 100.
func TestRemoteBar_Run_FailingTestsBlockIsCappedAtOneHundredLines(t *testing.T) {
	var lines []string
	for i := 0; i < 150; i++ {
		lines = append(lines, fmt.Sprintf("bar\trun\t2026-10-30T01:00:00.0000000Z --- FAIL: TestCap_%03d (0.01s)", i))
	}
	out, code, _, _ := runWithStub(t, "STUB_STATE=completed failure", "STUB_STEPS=Run committed bar", "STUB_FAILED_LOG="+failedLogFile(t, lines))

	require.Equal(t, 1, code, out)
	block := failingBlock(t, out)
	require.Len(t, block, 100, "exactly 100 lines")
	require.Equal(t, "--- FAIL: TestCap_000", block[0])
	require.Equal(t, "--- FAIL: TestCap_099", block[99])
	require.NotContains(t, strings.Join(block, "\n"), "TestCap_100")
}

// TestRemoteBar_Run_FailingTestsBlockMatchesEachMarkerAloneAndNothingElse pins
// every marker on its own line, and that bare FAIL, ok and lowercase lines
// stay out of the block.
func TestRemoteBar_Run_FailingTestsBlockMatchesEachMarkerAloneAndNothingElse(t *testing.T) {
	ts := "bar\trun\t2026-10-30T01:00:00.0000000Z "
	lines := []string{
		ts + "--- FAIL: TestMarker_Sub (0.01s)",
		ts + "FAIL\tpkg/one\t0.5s",
		ts + "panic: marker only",
		ts + "pkg/two [build failed]",
		ts + "test timed out after 10m0s",
		ts + "FAIL",
		ts + "ok  \tpkg/fine\t0.1s",
		ts + "fail: lowercase noise",
		ts + "FAIL\tpkg/one\t0.9s",
	}
	out, code, _, _ := runWithStub(t, "STUB_STATE=completed failure", "STUB_STEPS=Run committed bar", "STUB_FAILED_LOG="+failedLogFile(t, lines))

	require.Equal(t, 1, code, out)
	want := []string{"--- FAIL: TestMarker_Sub", "FAIL\tpkg/one", "panic: marker only", "pkg/two [build failed]", "test timed out after 10m0s"}
	require.Equal(t, want, failingBlock(t, out), "each marker once, durations ignored for dedup, noise excluded")
}

// TestRemoteBar_Run_FailingTestsBlockDeduplicatesBeforeTheCap pins MTIX-215.2:
// duplicates are collapsed BEFORE the 100-line cap, so repeated failure lines
// cannot use up cap slots and hide distinct ones. 60 copies of one failure are
// followed by 120 distinct failures; the block holds 100 distinct lines.
func TestRemoteBar_Run_FailingTestsBlockDeduplicatesBeforeTheCap(t *testing.T) {
	ts := "bar\trun\t2026-10-30T01:00:00.0000000Z "
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, ts+"--- FAIL: TestDup_Repeated (0.01s)")
	}
	for i := 0; i < 120; i++ {
		lines = append(lines, fmt.Sprintf("%s--- FAIL: TestDup_%03d (0.01s)", ts, i))
	}
	out, code, _, _ := runWithStub(t, "STUB_STATE=completed failure", "STUB_STEPS=Run committed bar", "STUB_FAILED_LOG="+failedLogFile(t, lines))

	require.Equal(t, 1, code, out)
	block := failingBlock(t, out)
	require.Len(t, block, 100, "exactly 100 lines")
	seen := map[string]bool{}
	for _, l := range block {
		require.False(t, seen[l], "no duplicate line in the block: %s", l)
		seen[l] = true
	}
	require.Equal(t, "--- FAIL: TestDup_Repeated", block[0])
	require.Equal(t, "--- FAIL: TestDup_098", block[99], "distinct lines past the repeats still fill the cap")
}

// TestRemoteBar_Run_FailingTestsBlockMatchingIsCaseSensitive pins that every
// marker matches in its documented case only: lowercase and mixed-case
// lookalikes stay out of the block.
func TestRemoteBar_Run_FailingTestsBlockMatchingIsCaseSensitive(t *testing.T) {
	ts := "bar\trun\t2026-10-30T01:00:00.0000000Z "
	lines := []string{
		ts + "--- fail: TestLower (0.01s)",
		ts + "fail\tpkg/lower\t0.5s",
		ts + "Fail\tpkg/mixed\t0.5s",
		ts + "PANIC: shouted",
		ts + "pkg/lower [Build Failed]",
		ts + "Test Timed Out after 10m0s",
		ts + "--- FAIL: TestReal (0.01s)",
	}
	out, code, _, _ := runWithStub(t, "STUB_STATE=completed failure", "STUB_STEPS=Run committed bar", "STUB_FAILED_LOG="+failedLogFile(t, lines))

	require.Equal(t, 1, code, out)
	require.Equal(t, []string{"--- FAIL: TestReal"}, failingBlock(t, out), "only the exact-case marker is listed")
}

// TestRemoteBar_Run_FailingTestsBlockFailMarkerNeedsALineOrWordStart pins the
// start anchor of the package FAIL marker: FAIL must begin the line or follow
// whitespace. FAIL glued to the end of another word is not a marker.
func TestRemoteBar_Run_FailingTestsBlockFailMarkerNeedsALineOrWordStart(t *testing.T) {
	ts := "bar\trun\t2026-10-30T01:00:00.0000000Z "
	lines := []string{
		ts + "NOFAIL\tpkg/glued\t0.5s",
		ts + "xFAIL pkg/glued2",
		ts + "FAIL\tpkg/start\t0.5s",
		ts + "note: FAIL pkg/afterspace",
	}
	out, code, _, _ := runWithStub(t, "STUB_STATE=completed failure", "STUB_STEPS=Run committed bar", "STUB_FAILED_LOG="+failedLogFile(t, lines))

	require.Equal(t, 1, code, out)
	want := []string{"FAIL\tpkg/start", "note: FAIL pkg/afterspace"}
	require.Equal(t, want, failingBlock(t, out), "line-start and after-whitespace FAIL list; glued FAIL does not")
}

// TestRemoteBar_Run_GreenRunPrintsNoFailedLog pins that the failed-log output
// is limited to failing runs.
func TestRemoteBar_Run_GreenRunPrintsNoFailedLog(t *testing.T) {
	lines := []string{"bar\trun\t2026-10-30T01:00:00.0000000Z --- FAIL: TestIgnored (0.01s)"}
	out, code, _, _ := runWithStub(t, "STUB_STATE=completed success", "STUB_FAILED_LOG="+failedLogFile(t, lines))

	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "failing tests:")
	require.NotContains(t, out, "log tail (last 200 lines):")
	require.NotContains(t, out, "TestIgnored")
}

// TestRemoteBar_Run_FailedRunLeavesNoTempFilesBehind pins that the captured
// failed log is removed with the temp dir.
func TestRemoteBar_Run_FailedRunLeavesNoTempFilesBehind(t *testing.T) {
	tmp := t.TempDir()
	lines := []string{"bar\trun\t2026-10-30T01:00:00.0000000Z --- FAIL: TestLeak (0.01s)"}
	out, code, _, _ := runWithStub(t, "STUB_STATE=completed failure", "STUB_STEPS=Run committed bar",
		"STUB_FAILED_LOG="+failedLogFile(t, lines), "TMPDIR="+tmp)

	require.Equal(t, 1, code, out)
	require.Contains(t, out, "TestLeak", "the failed log was captured and printed")
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, entries, "no remote-bar temp dir or failed.log is left behind")
}

func TestRemoteBar_Run_TimeoutCancelsTheRunAndReportsInfraFailure(t *testing.T) {
	out, code, bare, logPath := runWithStub(t, "STUB_STATE=in_progress ", "RB_TIMEOUT_SECS=1")
	require.Equal(t, 2, code, out)
	require.Contains(t, out, " workflow_ref=main workflow_sha=")
	require.Contains(t, out, "workflow_sha="+strings.Repeat("1", 40))
	require.Empty(t, git(t, bare, "for-each-ref", "refs/heads/ci-scratch"))
	logged, err := os.ReadFile(logPath) //nolint:gosec // test-owned path
	require.NoError(t, err)
	require.Contains(t, string(logged), "run cancel 4242")
}

func TestRemoteBar_Run_TermWhilePollingCleansUpPromptly(t *testing.T) {
	work, bare := repoWithRemote(t)
	bin, logPath := stubGh(t)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b"), 0o600))
	abs, err := filepath.Abs(scriptPath)
	require.NoError(t, err)
	snippet := `source "$RB_SCRIPT"; rb_remote_urls_ok() { return 0; }; rb_install_traps; rb_run "$S"`
	cmd := exec.Command("bash", "-c", snippet)
	cmd.Dir = work
	cmd.Env = cleanEnv("RB_SCRIPT="+abs, "S="+snap, "RB_REMOTE=origin", "STUB_LOG="+logPath,
		"STUB_STATE=in_progress ", "RB_POLL_SECS=60", "RB_TIMEOUT_SECS=3600", "PATH="+bin+":"+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	pgid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	require.Eventually(t, func() bool {
		b, readErr := os.ReadFile(logPath) //nolint:gosec // test-owned path
		return readErr == nil && strings.Contains(string(b), "run view")
	}, 30*time.Second, 100*time.Millisecond, "wrapper never reached the polling loop")
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("TERM during polling was not handled within 10s (poll interval is 60s)")
	}
	require.Empty(t, git(t, bare, "for-each-ref", "refs/heads/ci-scratch"), "scratch branch must be deleted")
	logged, err := os.ReadFile(logPath) //nolint:gosec // test-owned path
	require.NoError(t, err)
	require.Contains(t, string(logged), "run cancel 4242")
	// The background nap must be reaped: no member of the process group lives on.
	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
	}, 5*time.Second, 100*time.Millisecond, "an orphan sleep survived the wrapper")
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

func dryRunTree(t *testing.T, out string) string {
	t.Helper()
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "tree=") {
			return strings.TrimPrefix(f, "tree=")
		}
	}
	t.Fatalf("no tree in output: %s", out)
	return ""
}

func TestRemoteBar_Snapshot_LeavesOutLoopInjectedFilesUnlessMainTracksThem(t *testing.T) {
	work, _ := repoWithRemote(t) // main tracks a.txt only
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, "CLAUDE.md"), []byte("injected\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, "AGENTS.md"), []byte("injected\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, "sub.md"), []byte("keep\n"), 0o600))
	// The caller's info/exclude is empty here, so only the pathspec can drop them.
	out, code := bash(t, work, `bash "$RB_SCRIPT" --dry-run "$S"`, "S="+snap)
	require.Equal(t, 0, code, out)
	files := git(t, work, "ls-tree", "-r", "--name-only", dryRunTree(t, out))
	require.NotContains(t, files, "CLAUDE.md")
	require.NotContains(t, files, "AGENTS.md")
	require.Contains(t, files, "b.txt")
	require.Contains(t, files, "sub.md")
	require.NotContains(t, out, "WARNING", "known loop files are not reported as hidden")

	// Once origin/main tracks CLAUDE.md, an edit to it is a real change.
	require.NoError(t, os.WriteFile(filepath.Join(work, "CLAUDE.md"), []byte("tracked\n"), 0o600))
	git(t, work, "add", "CLAUDE.md")
	git(t, work, "commit", "-m", "track")
	git(t, work, "push", "origin", "main")
	git(t, work, "fetch", "origin")
	out, code = bash(t, work, `bash "$RB_SCRIPT" --dry-run "$S"`, "S="+snap)
	require.Equal(t, 0, code, out)
	require.Contains(t, git(t, work, "ls-tree", "-r", "--name-only", dryRunTree(t, out)), "CLAUDE.md")
	require.Equal(t, "injected", strings.TrimSpace(git(t, work, "show", dryRunTree(t, out)+":CLAUDE.md")))
}

func TestRemoteBar_Snapshot_WarnsAboutFilesHiddenOnlyByInfoExclude(t *testing.T) {
	work, _ := repoWithRemote(t)
	require.NoError(t, os.WriteFile(filepath.Join(work, ".git", "info", "exclude"), []byte("secret.txt\nhiddendir/\ngitignored.txt\nCLAUDE.md\n"), 0o600))
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, "secret.txt"), []byte("s\n"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(snap, "hiddendir"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(snap, "hiddendir", "f"), []byte("f"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, "gitignored.txt"), []byte("g"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, "CLAUDE.md"), []byte("injected"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(snap, ".gitignore"), []byte("gitignored.txt\n"), 0o600))
	out, code := bash(t, work, `bash "$RB_SCRIPT" --dry-run "$S"`, "S="+snap)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "WARNING not in the snapshot, hidden only by this repository's info/exclude")
	require.Contains(t, out, "secret.txt")
	require.Contains(t, out, "hiddendir/")
	require.NotContains(t, out, "gitignored.txt", "files the worktree .gitignore hides are expected")
	require.NotContains(t, out, "CLAUDE.md", "known loop-injected files are not reported")
}

// Make assigns GOFLAGS := -trimpath and exports it to go whenever GOFLAGS is
// already in the environment, which breaks every runtime.Caller-based test.
// The -p=1 setting therefore goes through go env -w, never through env.
func TestRemoteBarWorkflow_GoFlags_NeverSetInAnyEnvBlock(t *testing.T) {
	for i, line := range strings.Split(readWorkflow(t), "\n") {
		code := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if strings.HasPrefix(code, "GOFLAGS:") || strings.HasPrefix(code, "GOFLAGS=") {
			t.Errorf("workflow line %d sets GOFLAGS in an env block: %q", i+1, line)
		}
	}
}

func TestRemoteBarWorkflow_GoEnvWrite_PrecedesTheBar(t *testing.T) {
	// MTIX-112.17.27: only the macOS job still runs make test-race and so still
	// serializes through go env -w; the Linux jobs pass -p explicitly.
	wf := jobBlock(t, readWorkflow(t), "bar-macos")
	write := strings.Index(wf, "go env -w GOFLAGS=-p=1")
	bar := strings.Index(wf, "- name: Run committed bar")
	require.GreaterOrEqual(t, write, 0, "the -p=1 setting must be written with go env -w")
	require.Greater(t, bar, write, "go env -w must run before the committed bar")
}

// writeSnapFiles creates the given relative files (content "x\n") under dir.
func writeSnapFiles(t *testing.T, dir string, rel ...string) {
	t.Helper()
	for _, r := range rel {
		p := filepath.Join(dir, r)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o600))
	}
}

// snapshotFiles dry-runs the script on snap and returns the snapshot's files.
func snapshotFiles(t *testing.T, work, snap string) string {
	t.Helper()
	out, code := bash(t, work, `bash "$RB_SCRIPT" --dry-run "$S"`, "S="+snap)
	require.Equal(t, 0, code, out)
	return git(t, work, "ls-tree", "-r", "--name-only", dryRunTree(t, out))
}

func TestRemoteBar_Snapshot_ExcludesIgnoredAndPresentLoopMetadataDirs(t *testing.T) {
	cases := []struct {
		name      string
		gitignore string
	}{
		{"ignored .mgit and .hyperswe", ".mgit/\n.hyperswe/\n"},
		{"ignored .mgit only", ".mgit/\n"},
		{"present but not ignored", "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work, _ := repoWithRemote(t)
			snap := t.TempDir()
			writeSnapFiles(t, snap, "b.txt", ".mgit/store/x", ".hyperswe/receipts/r")
			require.NoError(t, os.WriteFile(filepath.Join(snap, ".gitignore"), []byte(tc.gitignore), 0o600))
			files := snapshotFiles(t, work, snap)
			require.Contains(t, files, "b.txt")
			require.NotContains(t, files, ".mgit")
			require.NotContains(t, files, ".hyperswe")
		})
	}
}

func TestRemoteBar_Snapshot_LoopRoundWorktreeLayout_SnapshotsCleanly(t *testing.T) {
	work, _ := repoWithRemote(t)
	snap := t.TempDir()
	// A loop round worktree: mgit store, hyperswe state, the repository
	// .gitignore listing them, and the injected root files.
	writeSnapFiles(t, snap, "b.txt", "sub/c.go", ".mgit/objects/o", ".hyperswe/data/d", "CLAUDE.md", "AGENTS.md")
	require.NoError(t, os.WriteFile(filepath.Join(snap, ".gitignore"), []byte("# mgit local store\n.mgit/\n.hyperswe/\n"), 0o600))
	files := snapshotFiles(t, work, snap)
	for _, keep := range []string{"b.txt", "sub/c.go", ".gitignore"} {
		require.Contains(t, files, keep)
	}
	for _, drop := range []string{".mgit", ".hyperswe", ".git/", "CLAUDE.md", "AGENTS.md"} {
		require.NotContains(t, files, drop)
	}
}
