// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The remote-bar verdict identifies the workflow source, and a changed bar
// workflow cannot be mistaken for a tested workflow (MTIX-112.17.30).
package scripts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoteBar_WorkflowSource_UnknownMetadataCannotYieldGreen(t *testing.T) {
	for _, extra := range []string{"STUB_WORKFLOW_REF=ci-scratch/other", "STUB_WORKFLOW_REF=", "STUB_WORKFLOW_SHA=", "STUB_WORKFLOW_SHA=short", "STUB_FAIL_SOURCE=1"} {
		t.Run(extra, func(t *testing.T) {
			out, code, _, _ := runWithStub(t, extra)
			require.Equal(t, 2, code, out)
			require.Contains(t, out, "workflow_ref=unknown")
			require.Contains(t, out, "requested_workflow_ref=main")
			require.NotContains(t, out, "conclusion=success")
		})
	}
}

func TestRemoteBar_WorkflowSource_ReuseNamesOriginalMainCommit(t *testing.T) {
	out, code, logged := reuseCase{mk: one(same, "success", "completed", hour)}.run(t)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "verdict=REUSED")
	require.Contains(t, out, " workflow_ref=main workflow_sha=")
	require.Contains(t, out, "workflow_sha="+strings.Repeat("0", 39)+"1")
	require.NotContains(t, logged, "workflow run")
}

func TestRemoteBar_WorkflowSource_ReuseRejectsUnknownOrContradictorySource(t *testing.T) {
	for _, field := range []string{"missing branch", "wrong branch", "missing sha", "short sha"} {
		t.Run(field, func(t *testing.T) {
			mk := func(k reuseKey) []string {
				var entry map[string]any
				require.NoError(t, json.Unmarshal([]byte(reuseRun(1, k.title(), "success", "completed", hour)), &entry))
				switch field {
				case "missing branch":
					delete(entry, "headBranch")
				case "wrong branch":
					entry["headBranch"] = "ci-scratch/other"
				case "missing sha":
					delete(entry, "headSha")
				case "short sha":
					entry["headSha"] = "short"
				}
				raw, err := json.Marshal(entry)
				require.NoError(t, err)
				return []string{string(raw)}
			}
			out, code, logged := reuseCase{mk: mk}.run(t)
			require.Equal(t, 0, code, out)
			require.NotContains(t, out, "REUSED")
			require.Contains(t, logged, "workflow run")
		})
	}
}

func barWorkflowSnapshot(t *testing.T, mutation string) (string, string, string, string, string) {
	t.Helper()
	work, bare := repoWithRemote(t)
	if mutation != "added" {
		require.NoError(t, os.MkdirAll(filepath.Join(work, ".github", "workflows"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(work, ".github", "workflows", "remote-bar.yml"), []byte("name: old\non: workflow_dispatch\n"), 0o600))
		git(t, work, "add", "-A")
		git(t, work, "commit", "-m", "workflow base")
		git(t, work, "push", "origin", "main")
		git(t, work, "fetch", "origin")
	}
	hook := `#!/bin/sh
printf '%s\n' "$1" >> push-events
`
	require.NoError(t, os.WriteFile(filepath.Join(bare, "hooks", "update"), []byte(hook), 0o700)) //nolint:gosec // test-owned executable push recorder
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "a.txt"), []byte("a\n"), 0o600))
	if mutation != "deleted" {
		require.NoError(t, os.MkdirAll(filepath.Join(snap, ".github", "workflows"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(snap, ".github", "workflows", "remote-bar.yml"), []byte("name: changed\non: workflow_dispatch\n"), 0o600))
	}
	bin, logPath := stubGh(t)
	return work, bare, snap, bin, logPath
}

func TestRemoteBar_WorkflowSource_BarWorkflowDifferenceIsRefusedBeforeSideEffects(t *testing.T) {
	for _, mutation := range []string{"changed", "deleted", "added"} {
		for _, flags := range []string{"", "--allow-workflow-diff", "--dry-run", "--dry-run --allow-workflow-diff"} {
			t.Run(mutation+flags, func(t *testing.T) {
				work, bare, snap, bin, logPath := barWorkflowSnapshot(t, mutation)
				snippet := `source "$RB_SCRIPT"; rb_remote_urls_ok() { return 0; }; rb_install_traps; rb_run ` + flags + ` "$S"`
				out, code := bash(t, work, snippet, "S="+snap, "PATH="+bin+":"+os.Getenv("PATH"), "STUB_LOG="+logPath)
				require.Equal(t, 2, code, out)
				require.Contains(t, out, "conclusion=not_run ")
				require.Contains(t, out, "cannot judge a changed remote-bar.yml")
				require.Contains(t, out, "no workflow ran")
				require.Contains(t, out, "workflow_ref=not-run")
				require.Contains(t, out, "workflow_sha=none")
				require.NoFileExists(t, filepath.Join(bare, "push-events"), "must refuse before even a temporary push")
				require.NoFileExists(t, logPath, "must refuse before dispatch or reuse lookup")
			})
		}
	}
}

func TestRemoteBar_WorkflowSource_OtherWorkflowDiffWarnsWithoutChangingSource(t *testing.T) {
	out, code, logged := reuseCase{args: "--allow-workflow-diff", withFlow: true}.run(t)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "WARNING")
	require.Contains(t, out, "not exercised")
	require.Contains(t, out, " workflow_ref=main workflow_sha=")
	require.Contains(t, logged, "workflow run remote-bar.yml --repo hyper-swe/mtix --ref main")
}

func TestRemoteBar_WorkflowSource_PushRecorderDetectsPush(t *testing.T) {
	work, bare, _, _, _ := barWorkflowSnapshot(t, "added")
	git(t, work, "push", "origin", "HEAD:refs/heads/ci-scratch/probe")
	raw, err := os.ReadFile(filepath.Join(bare, "push-events"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "refs/heads/ci-scratch/probe")
}

func TestRemoteBar_WorkflowSource_DispatchFailureDoesNotClaimExecution(t *testing.T) {
	out, code, _, _ := runWithStub(t, "STUB_FAIL_DISPATCH=1")
	require.Equal(t, 2, code, out)
	require.Contains(t, out, "conclusion=error ")
	require.Contains(t, out, "workflow_ref=unknown")
	require.Contains(t, out, "workflow_sha=unknown")
	require.Contains(t, out, "requested_workflow_ref=main")
	require.NotContains(t, out, "conclusion=success")
}

func TestRemoteBar_WorkflowSource_PreflightFailureNamesNoWorkflow(t *testing.T) {
	out, code := bash(t, t.TempDir(), `bash "$RB_SCRIPT" --unknown-option`)
	require.Equal(t, 2, code, out)
	require.Contains(t, out, "conclusion=not_run ")
	require.Contains(t, out, "workflow_ref=not-run")
	require.Contains(t, out, "workflow_sha=none")
}

// An unexpected shell exit after dispatch must still emit a truthful verdict.
func TestRemoteBar_WorkflowSource_UnexpectedExitAfterPushEmitsFallbackVerdict(t *testing.T) {
	work, bare := repoWithRemote(t)
	bin, logPath := stubGh(t)
	snap := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snap, "b.txt"), []byte("b"), 0o600))
	snippet := `source "$RB_SCRIPT"; rb_remote_urls_ok() { return 0; }; rb_find_run() { exit 19; }; rb_install_traps; rb_run "$S"`
	out, code := bash(t, work, snippet, "S="+snap, "PATH="+bin+":"+os.Getenv("PATH"), "STUB_LOG="+logPath)
	require.Equal(t, 19, code, out)
	require.Contains(t, out, "conclusion=error failed_steps=none exit=19 workflow_ref=unknown workflow_sha=unknown requested_workflow_ref=main")
	logged, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(logged), "workflow run remote-bar.yml")
	require.Empty(t, git(t, bare, "for-each-ref", "refs/heads/ci-scratch"), "pushed scratch branch must be deleted after unexpected exit")
}
