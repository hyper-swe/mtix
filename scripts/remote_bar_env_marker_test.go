// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-262: when the scratch-branch push or the workflow dispatch fails with
// HTTP 5xx, HTTP 429 or a network error, the remote-bar wrapper ends its
// output with the one line the HyperSWE loop reads as a host failure:
//
//	hyperswe: environment: <cause>
//
// It is the last non-empty line (after the scratch-branch cleanup warning),
// the cause is at most 200 characters, and nothing prints it on exit 0 or for
// any other failure (auth, ref refusal, tree mismatch, red bar, infra).
package scripts_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const envMarkerPrefix = "hyperswe: environment: "

// lastLine returns the last non-empty line of out.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n \t"), "\n")
	return lines[len(lines)-1]
}

// stubPushGit writes a git wrapper into binDir that fails every push which is
// not a delete with STUB_PUSH_ERR on stderr, fails a delete when STUB_DELETE_FAIL
// is set, and runs the real git otherwise.
func stubPushGit(t *testing.T, binDir string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	script := `#!/bin/sh
case " $* " in
*" push "*)
  case " $* " in
  *" --delete "*) [ -n "$STUB_DELETE_FAIL" ] && { echo "fatal: delete refused" >&2; exit 1; } ;;
  *)
    if [ -n "$STUB_PUSH_ERR" ]; then printf '%s\n' "$STUB_PUSH_ERR" >&2; exit 1; fi ;;
  esac ;;
esac
exec ` + realGit + ` "$@"
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o700)) //nolint:gosec // test stub must be executable
}

// runEnvStub runs the wrapper against the stubs with the git push stub armed.
func runEnvStub(t *testing.T, env ...string) (out string, code int) {
	t.Helper()
	work, _ := repoWithRemote(t)
	bin, logPath := stubGh(t)
	stubPushGit(t, bin)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b"), 0o600))
	snippet := `source "$RB_SCRIPT"; rb_remote_urls_ok() { return 0; }; rb_install_traps; rb_run --ticket MTIX-262 "$S"`
	all := append([]string{"S=" + snap, "RB_REMOTE=origin", "STUB_LOG=" + logPath, "RB_POLL_SECS=1",
		"PATH=" + bin + ":" + os.Getenv("PATH")}, env...)
	return bash(t, work, snippet, all...)
}

func TestRemoteBar_EnvMarker_PushFailure_PrintsMarkerAsFinalLine(t *testing.T) {
	cases := []struct {
		name, err, cause string
	}{
		{"http 500", "error: RPC failed; HTTP 500 curl 22 The requested URL returned error: 500", "GitHub returned HTTP 500 on the scratch-branch push"},
		{"http 502", "fatal: unable to access 'https://github.com/hyper-swe/mtix/': The requested URL returned error: 502", "GitHub returned HTTP 502 on the scratch-branch push"},
		{"http 503 over HTTP/2", "remote: HTTP/2 503 Service Unavailable", "GitHub returned HTTP 503 on the scratch-branch push"},
		{"http 429", "remote: error: HTTP 429 too many requests", "GitHub returned HTTP 429 on the scratch-branch push"},
		{"connection reset", "fatal: unable to access 'https://github.com/hyper-swe/mtix/': Recv failure: Connection reset by peer", "network failure reaching GitHub on the scratch-branch push"},
		{"could not resolve host", "fatal: unable to access 'https://github.com/hyper-swe/mtix/': Could not resolve host: github.com", "network failure reaching GitHub on the scratch-branch push"},
		{"ssh connection timed out", "ssh: connect to host github.com port 22: Connection timed out", "network failure reaching GitHub on the scratch-branch push"},
		{"remote end hung up", "fatal: the remote end hung up unexpectedly", "network failure reaching GitHub on the scratch-branch push"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runEnvStub(t, "STUB_PUSH_ERR="+tc.err)
			require.Equal(t, 2, code, out)
			require.Equal(t, envMarkerPrefix+tc.cause, lastLine(out), out)
			require.Equal(t, 1, strings.Count(out, envMarkerPrefix), "printed once: %s", out)
			require.True(t, strings.HasSuffix(out, "\n"), "marker line is newline terminated")
		})
	}
}

func TestRemoteBar_EnvMarker_PushFailure_MarkerFollowsTheCleanupWarning(t *testing.T) {
	out, code := runEnvStub(t, "STUB_PUSH_ERR=error: RPC failed; HTTP 500 curl 22", "STUB_DELETE_FAIL=1")
	require.Equal(t, 2, code, out)
	warn := strings.Index(out, "WARNING could not delete")
	require.GreaterOrEqual(t, warn, 0, "STUB_DELETE_FAIL makes the branch delete fail, so the warning prints: %s", out)
	require.Less(t, warn, strings.Index(out, envMarkerPrefix), "marker comes after the warning: %s", out)
	require.Equal(t, envMarkerPrefix+"GitHub returned HTTP 500 on the scratch-branch push", lastLine(out))
}

func TestRemoteBar_EnvMarker_PushFailure_OtherFailuresPrintNoMarker(t *testing.T) {
	cases := []struct{ name, err string }{
		{"auth 403", "fatal: unable to access 'https://github.com/hyper-swe/mtix/': The requested URL returned error: 403"},
		{"auth 401 mentioning a connection", "remote: HTTP 401 unauthorized; connection reset after login"},
		{"ref refusal 422", "remote: error: HTTP 422 unprocessable; refusing to update ref"},
		{"rejected by a rule", "! [remote rejected] x -> ci-scratch/x (push declined due to repository rule violations)"},
		{"not found 404", "remote: Repository not found. fatal: HTTP 404"},
		{"empty message", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runEnvStub(t, "STUB_PUSH_ERR="+tc.err)
			if tc.err == "" {
				require.Equal(t, 0, code, "an empty stub message disarms the push failure: %s", out)
				return
			}
			require.Equal(t, 2, code, out)
			require.NotContains(t, out, "hyperswe: environment:")
		})
	}
}

func TestRemoteBar_EnvMarker_DispatchFailure_PrintsMarkerAsFinalLine(t *testing.T) {
	cases := []struct {
		name, msg, cause string
	}{
		{"http 500", "HTTP 500: Server Error (https://api.github.com/repos/hyper-swe/mtix/actions/workflows/remote-bar.yml/dispatches)", "GitHub returned HTTP 500 on the workflow dispatch"},
		{"http 502", "HTTP 502 bad gateway", "GitHub returned HTTP 502 on the workflow dispatch"},
		{"http 429", "HTTP 429: API rate limit exceeded", "GitHub returned HTTP 429 on the workflow dispatch"},
		{"connection reset", "error connecting to api.github.com: read tcp: connection reset by peer", "network failure reaching GitHub on the workflow dispatch"},
		{"dns", "dial tcp: lookup api.github.com: no such host", "network failure reaching GitHub on the workflow dispatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runEnvStub(t, "STUB_FAIL_DISPATCH=1", "STUB_DISPATCH_MSG="+tc.msg)
			require.Equal(t, 2, code, out)
			require.Equal(t, envMarkerPrefix+tc.cause, lastLine(out), out)
			require.Equal(t, 1, strings.Count(out, envMarkerPrefix), out)
		})
	}
}

func TestRemoteBar_EnvMarker_DispatchWithoutBaseFallback_PrintsMarkerOnServerError(t *testing.T) {
	out, code := runEnvStub(t, "STUB_REJECT_BASE=1", "STUB_FAIL_NOBASE=HTTP 503: Service Unavailable")
	require.Equal(t, 2, code, out)
	require.Equal(t, envMarkerPrefix+"GitHub returned HTTP 503 on the workflow dispatch", lastLine(out), out)
	out, code = runEnvStub(t, "STUB_REJECT_BASE=1", "STUB_FAIL_NOBASE=HTTP 403: Resource not accessible by integration")
	require.Equal(t, 2, code, out)
	require.NotContains(t, out, "hyperswe: environment:")
}

func TestRemoteBar_EnvMarker_DispatchFailure_OtherFailuresPrintNoMarker(t *testing.T) {
	for _, msg := range []string{
		"HTTP 403: Resource not accessible by integration",
		"HTTP 422: Workflow does not have 'workflow_dispatch' trigger",
		"HTTP 404: Not Found",
		"could not create workflow dispatch event: bad input",
	} {
		out, code := runEnvStub(t, "STUB_FAIL_DISPATCH=1", "STUB_DISPATCH_MSG="+msg)
		require.Equal(t, 2, code, out)
		require.NotContains(t, out, "hyperswe: environment:", msg)
	}
}

func TestRemoteBar_EnvMarker_NonEnvironmentalOutcomes_PrintNoMarker(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want int
	}{
		{"green", []string{"STUB_STATE=completed success"}, 0},
		{"bar red", []string{"STUB_STATE=completed failure", "STUB_STEPS=Run committed bar"}, 1},
		{"tree mismatch", []string{"STUB_STATE=completed failure", "STUB_STEPS=Verify snapshot tree"}, 3},
		{"cancelled run", []string{"STUB_STATE=completed cancelled"}, 2},
		{"timeout", []string{"STUB_STATE=in_progress ", "RB_TIMEOUT_SECS=1"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runEnvStub(t, tc.env...)
			require.Equal(t, tc.want, code, out)
			require.NotContains(t, out, "hyperswe: environment:")
		})
	}
}

func TestRemoteBar_EnvMarker_RedBarWhoseLogMentionsAServerError_PrintsNoMarker(t *testing.T) {
	logFile := failedLogFile(t, []string{"--- FAIL: TestX", "HTTP 500 from a test fixture", "hyperswe: environment: quoted by a test"})
	out, code := runEnvStub(t, "STUB_STATE=completed failure", "STUB_STEPS=Run committed bar", "STUB_FAILED_LOG="+logFile)
	require.Equal(t, 1, code, out)
	require.Equal(t, 1, strings.Count(out, envMarkerPrefix), "only the quoted log line, not one of ours: %s", out)
	require.NotEqual(t, envMarkerPrefix+"GitHub returned HTTP 500 on the scratch-branch push", lastLine(out))
}

func TestRemoteBar_EnvMarker_Emit_TruncatesCauseAndSkipsExitZero(t *testing.T) {
	long := strings.Repeat("x", 300)
	out, code := bash(t, t.TempDir(), `source "$RB_SCRIPT"; RB_ENV_CAUSE="$C"; rb_env_emit 2`, "C="+long)
	require.Equal(t, 0, code, out)
	require.Equal(t, envMarkerPrefix+strings.Repeat("x", 200)+"\n", out)

	out, _ = bash(t, t.TempDir(), `source "$RB_SCRIPT"; RB_ENV_CAUSE="$C"; rb_env_emit 0`, "C=cause")
	require.Empty(t, out, "never on exit 0")
	out, _ = bash(t, t.TempDir(), `source "$RB_SCRIPT"; rb_env_emit 2`)
	require.Empty(t, out, "never without a recorded cause")

	out, _ = bash(t, t.TempDir(), `source "$RB_SCRIPT"; RB_ENV_CAUSE="$(printf 'a\nb\tc')"; rb_env_emit 2`)
	require.Equal(t, envMarkerPrefix+"abc\n", out, "one line, control characters removed")
}

func TestRemoteBar_EnvMarker_Classify_FirstHTTPStatusDecides(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"HTTP 500", "GitHub returned HTTP 500 on the scratch-branch push"},
		{"HTTP 599 x", "GitHub returned HTTP 599 on the scratch-branch push"},
		{"HTTP 499", ""},
		{"HTTP 404 then HTTP 500", ""},
		{"HTTP 500 then HTTP 403", "GitHub returned HTTP 500 on the scratch-branch push"},
		{"HTTP 600", ""},
		{"port 5000 in use", ""},
		{"", ""},
	}
	for _, tc := range cases {
		out, code := bash(t, t.TempDir(), `source "$RB_SCRIPT"; rb_env_classify push "$T"`, "T="+tc.text)
		if tc.want == "" {
			require.Equal(t, 1, code, tc.text)
			require.Empty(t, out, tc.text)
			continue
		}
		require.Equal(t, 0, code, tc.text)
		require.Equal(t, tc.want+"\n", out, tc.text)
	}
}

func TestRemoteBar_EnvMarker_CombinedStreams_EndWithTheMarkerLine(t *testing.T) {
	work, _ := repoWithRemote(t)
	bin, logPath := stubGh(t)
	stubPushGit(t, bin)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b"), 0o600))
	snippet := `source "$RB_SCRIPT"; rb_remote_urls_ok() { return 0; }; rb_install_traps; rb_run --ticket MTIX-262 "$S"`
	cmd := exec.Command("bash", "-c", snippet+" 2>&1")
	cmd.Dir = work
	cmd.Env = cleanEnv("RB_SCRIPT="+mustAbs(t, scriptPath), "S="+snap, "RB_REMOTE=origin", "STUB_LOG="+logPath,
		"STUB_PUSH_ERR=error: RPC failed; HTTP 500 curl 22", "STUB_DELETE_FAIL=1", "PATH="+bin+":"+os.Getenv("PATH"))
	b, _ := cmd.CombinedOutput()
	require.Equal(t, envMarkerPrefix+"GitHub returned HTTP 500 on the scratch-branch push", lastLine(string(b)), string(b))
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	require.NoError(t, err)
	return abs
}

func TestRemoteBar_EnvMarker_StderrIsEmptyAfterTheMarker(t *testing.T) {
	work, _ := repoWithRemote(t)
	bin, logPath := stubGh(t)
	stubPushGit(t, bin)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b"), 0o600))
	snippet := `source "$RB_SCRIPT"; rb_remote_urls_ok() { return 0; }; rb_install_traps; rb_run --ticket MTIX-262 "$S"`
	cmd := exec.Command("bash", "-c", snippet)
	cmd.Dir = work
	cmd.Env = cleanEnv("RB_SCRIPT="+mustAbs(t, scriptPath), "S="+snap, "RB_REMOTE=origin", "STUB_LOG="+logPath,
		"STUB_PUSH_ERR=error: RPC failed; HTTP 500 curl 22", "STUB_DELETE_FAIL=1", "PATH="+bin+":"+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	_ = cmd.Run()
	require.Contains(t, stdout.String(), envMarkerPrefix)
	require.NotContains(t, stderr.String(), "WARNING could not delete", "cleanup output goes to stdout, before the marker")
	require.Equal(t, envMarkerPrefix+"GitHub returned HTTP 500 on the scratch-branch push", lastLine(stdout.String()))
}
