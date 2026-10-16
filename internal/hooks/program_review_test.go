// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func reviewOf(t *testing.T, command string) ProgramReview {
	t.Helper()
	f := newPrepareFixture(t, command)
	review, err := f.approvals.ReviewProgram(f.hook(t))
	require.NoError(t, err)
	return review
}

func TestReviewProgram_PinnedScriptAndInterpreterNeedNoReview(t *testing.T) {
	for _, tc := range []struct{ command, class string }{
		{"[scripts/run.sh]", ClassPinnedScript},
		{"[env, A=1, scripts/run.sh]", ClassPinnedScript},
		{"[sh, scripts/run.sh]", ClassInterpreterScript},
		{"[env, sh, scripts/run.sh]", ClassInterpreterScript},
		{"[SH, scripts/run.sh]", ClassInterpreterScript},
		{"[Bash.EXE, scripts/run.sh]", ClassInterpreterScript},
		{"[bash.cmd, scripts/run.sh]", ClassInterpreterScript},
		{"[python3.BAT, scripts/run.sh]", ClassInterpreterScript},
	} {
		t.Run(tc.command, func(t *testing.T) {
			review := reviewOf(t, tc.command)
			require.Equal(t, tc.class, review.Class)
			require.False(t, review.Needed)
			require.Empty(t, review.Fix)
		})
	}
}

func TestReviewProgram_OtherProgramNeedsReview(t *testing.T) {
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
	for _, command := range []string{
		"[cat, data/x]",
		"[env, cat, data/x]",
		"[find, ., -name, x]",
		"[sh]",
	} {
		t.Run(command, func(t *testing.T) {
			f := newPrepareFixture(t, command)
			review, err := f.approvals.ReviewProgram(f.hook(t))
			require.NoError(t, err)
			require.Equal(t, ClassOther, review.Class)
			require.True(t, review.Needed)
			require.Contains(t, review.Fix, "pinned project script")
			require.Contains(t, review.Fix, TrustFix("wake"))
			require.NotEmpty(t, review.Detail)
		})
	}
	f := newPrepareFixture(t, "[cat, data/x]")
	review, err := f.approvals.ReviewProgram(f.hook(t))
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(cat)
	require.NoError(t, err)
	require.Equal(t, want, review.Program, "the program is reported by its symlink-resolved path")
}

func TestReviewProgram_ScriptOutsideProjectIsOther(t *testing.T) {
	f := newPrepareFixture(t, "[sh, /etc/hosts]")
	review, err := f.approvals.ReviewProgram(f.hook(t))
	require.NoError(t, err)
	require.Equal(t, ClassOther, review.Class, "an interpreter running a script that is not pinned is not reviewed-free")
}

func TestApproveSelected_ReturnsTheReview(t *testing.T) {
	f := newFixture(t, "[cat, data/x]", fixtureOptions{unapproved: true})
	review, err := f.approvals.ApproveSelected(f.hook(t), true)
	require.NoError(t, err)
	require.True(t, review.Needed)

	g := newFixture(t, "[sh, scripts/run.sh]", fixtureOptions{unapproved: true})
	review, err = g.approvals.ApproveSelected(g.hook(t), true)
	require.NoError(t, err)
	require.False(t, review.Needed)

	h := newFixture(t, "[cat, data/x]", fixtureOptions{unapproved: true})
	_, err = h.approvals.ApproveSelected(h.hook(t), false)
	require.Error(t, err, "a refused approval returns no review")
}

func TestCheck_DecisionCarriesTheProgramReviewJSONShape(t *testing.T) {
	f := newPrepareFixture(t, "[cat, data/x]")
	decisions, err := f.approvals.Check()
	require.NoError(t, err)
	raw, err := json.Marshal(decisions["wake"])
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	review, ok := got["program_review"].(map[string]any)
	require.True(t, ok, "program_review is present for an exec hook")
	require.ElementsMatch(t, []string{"class", "review_needed", "program", "detail", "fix"}, keysOf(review))
	require.Equal(t, "other", review["class"])
	require.Equal(t, true, review["review_needed"])

	g := newPrepareFixture(t, "[sh, scripts/run.sh]")
	decisions, err = g.approvals.Check()
	require.NoError(t, err)
	raw, err = json.Marshal(decisions["wake"])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &got))
	review = got["program_review"].(map[string]any)
	require.Equal(t, "interpreter-script", review["class"])
	require.Equal(t, false, review["review_needed"])
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestLaunchPlanUnchangedByTheReview(t *testing.T) {
	f := newPrepareFixture(t, "[cat, data/x]")
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err, "the notice is advisory: launch behaviour is unchanged")
	require.NotEmpty(t, plan.Argv)
}

func TestReviewProgram_InterpreterClassIsNarrow(t *testing.T) {
	t.Setenv("PATH", "/usr/bin"+string(os.PathListSeparator)+"/bin"+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct{ command, class string }{
		{"[python3, scripts/run.sh]", ClassInterpreterScript},
		{"[bash, scripts/run.sh]", ClassInterpreterScript},
		{"[/usr/bin/python3, scripts/run.sh]", ClassInterpreterScript},
		{"[env, /usr/bin/bash, scripts/run.sh]", ClassInterpreterScript},
		{"[/tmp/evil/python3, scripts/run.sh]", ClassOther},
		{"[../outside/bash, scripts/run.sh]", ClassOther},
		{"[env, /opt/x/node, scripts/run.sh]", ClassOther},
		{"[bash-anything, scripts/run.sh]", ClassOther},
		{"[python3.exe.bak, scripts/run.sh]", ClassOther},
		{"[python3.11-intel64, scripts/run.sh]", ClassOther},
		{"[./local/bash, scripts/run.sh]", ClassOther},
	} {
		t.Run(tc.command, func(t *testing.T) {
			review := reviewOf(t, tc.command)
			require.Equal(t, tc.class, review.Class)
			require.Equal(t, tc.class == ClassOther, review.Needed)
		})
	}
}

func TestReviewProgram_ProgramLinkIsReportedByItsResolvedPath(t *testing.T) {
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(cat)
	require.NoError(t, err)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(dir, "mytool")
	require.NoError(t, os.Symlink(want, link))
	review := reviewOf(t, "["+link+", data/x]")
	require.Equal(t, ClassOther, review.Class)
	require.Equal(t, want, review.Program, "a link outside the project is reported on its resolved path, not the link")
}

func TestLaunchNameMatchingIsUnchangedByTheReview(t *testing.T) {
	got, err := programIndex([]string{"nohup.cmd", "scripts/run.sh"})
	require.NoError(t, err, "as on the base, nohup.cmd is not a wrapper name for launch")
	require.Equal(t, 0, got)
	got, err = programIndex([]string{"env.cmd", "A.B=1", "sh", "scripts/run.sh"})
	require.NoError(t, err, "as on the base, env.cmd is an ordinary program for launch")
	require.Equal(t, 0, got)
	got, err = programIndex([]string{"timeout.bat", "5", "sh"})
	require.NoError(t, err)
	require.Equal(t, 0, got)
	_, err = programIndex([]string{"nohup.exe", "sh"})
	require.Error(t, err, "the base still strips .exe")
	require.Equal(t, "nohup", programBase("nohup.EXE"))
	require.Equal(t, "nohup.cmd", programBase("nohup.cmd"))
}

func TestApproveSelected_UnclassifiableHookFailsClosed(t *testing.T) {
	f := newFixture(t, "[sh, scripts/run.sh]", fixtureOptions{unapproved: true})
	f.approvals.classify = func(Hook) (ProgramReview, error) { return ProgramReview{}, errors.New("boom") }
	review, err := f.approvals.ApproveSelected(f.hook(t), true)
	require.NoError(t, err, "the approval itself succeeded")
	require.Equal(t, ClassUnknown, review.Class)
	require.True(t, review.Needed)
	require.Contains(t, review.Detail, "could not be classified")
	require.Contains(t, review.Detail, "boom")
	require.Contains(t, review.Fix, TrustFix("wake"))

	decisions, err := f.approvals.Check()
	require.NoError(t, err)
	require.NotNil(t, decisions["wake"].ProgramReview)
	require.Equal(t, ClassUnknown, decisions["wake"].ProgramReview.Class)
}
