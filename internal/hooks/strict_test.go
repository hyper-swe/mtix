// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// strictFixture builds an approved hook running an unpinned program (cat) with
// strict mode switched on through the operator path.
func strictFixture(t *testing.T, command string) prepareFixture {
	t.Helper()
	f := newPrepareFixture(t, command)
	require.NoError(t, f.approvals.SetStrict(true, true))
	return f
}

func TestStrict_Off_LaunchBehaviourUnchanged(t *testing.T) {
	f := newPrepareFixture(t, "[cat, data/x]")
	on, err := f.approvals.StrictEnabled()
	require.NoError(t, err)
	require.False(t, on, "strict mode is off by default")
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.NotEmpty(t, plan.Argv)
}

func TestStrict_On_RefusesUnlistedProgramNamingTheRule(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	_, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "strict program mode")
	require.Contains(t, err.Error(), ProgramTrustFix("wake"))
}

func TestStrict_On_AllowsApprovedProgramAndPinsItsResolvedPath(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	program, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.Equal(t, program, plan.Argv[0], "the launched path is the approved, symlink-resolved one")
}

func TestStrict_On_PinnedScriptsAndInterpretersNeedNoProgramApproval(t *testing.T) {
	for _, command := range []string{"[scripts/run.sh]", "[sh, scripts/run.sh]", "[env, sh, scripts/run.sh]"} {
		t.Run(command, func(t *testing.T) {
			f := strictFixture(t, command)
			_, err := f.approvals.PrepareExec(f.hook(t))
			require.NoError(t, err)
		})
	}
}

func TestStrict_On_EnvWrappedProgramIsJudgedByTheProgram(t *testing.T) {
	f := strictFixture(t, "[env, cat, data/x]")
	_, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	_, err = f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
}

func TestApproveProgram_RefusesProgramResolvingInsideTheProject(t *testing.T) {
	link := filepath.Join(t.TempDir(), "tool")
	f := newFixtureWithScript(t, "["+link+", x]", "bin/tool", "#!/bin/sh\n")
	require.NoError(t, os.Symlink(filepath.Join(f.root, "bin", "tool"), link))
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "inside the project")
}

func TestApproveProgram_ScriptAndInterpreterHooksNeedNoApproval(t *testing.T) {
	f := newPrepareFixture(t, "[sh, scripts/run.sh]")
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "needs no program approval")
}

func TestApproveProgram_LinkToApprovedPathResolvesToTheSameRecord(t *testing.T) {
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
	resolvedCat, err := filepath.EvalSymlinks(cat)
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "mycat")
	require.NoError(t, os.Symlink(resolvedCat, link))
	f := strictFixture(t, "["+link+", data/x]")
	got, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	require.Equal(t, resolvedCat, got)
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err, "launch through the link matches the record for the resolved path")
	require.Equal(t, resolvedCat, plan.Argv[0], "the link is replaced by the approved resolved path, so a later retarget cannot redirect the launch")
}

func TestStrict_On_RetargetedLinkVoidsTheApproval(t *testing.T) {
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
	dir := t.TempDir()
	link := filepath.Join(dir, "prog")
	require.NoError(t, os.Symlink(cat, link))
	f := strictFixture(t, "["+link+", data/x]")
	_, err = f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	other, err := exec.LookPath("true")
	require.NoError(t, err)
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(other, link))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "a link that now resolves elsewhere is not the approved program")
}

func TestStrict_On_ChangingTheHookVoidsTheProgramApproval(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	yaml := "hooks:\n  - name: wake\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: " + execValue("[cat, data/y]") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "hooks.yaml"), []byte(yaml), 0o600))
	require.NoError(t, f.approvals.Approve("wake", true), "the hook is approved again, the program is not")
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
}

func TestStrict_AgentSessionAndMissingTerminalCannotChangeIt(t *testing.T) {
	f := newPrepareFixture(t, "[cat, data/x]")
	require.NoError(t, f.approvals.SetStrict(true, true))
	agent := NewApprovals(f.mtixDir, f.state, true)
	require.Error(t, agent.SetStrict(false, true), "refused under an agent session marker")
	require.Error(t, f.approvals.SetStrict(false, false), "refused without a terminal")
	_, err := agent.ApproveProgram(f.hook(t), true)
	require.Error(t, err, "an agent cannot approve a program")
	_, err = f.approvals.ApproveProgram(f.hook(t), false)
	require.Error(t, err)
	on, err := f.approvals.StrictEnabled()
	require.NoError(t, err)
	require.True(t, on)
}

func TestStrict_CorruptStateFailsClosed(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	dir, err := f.state.Path()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Type().IsRegular() && e.Name() != "host-id" {
			data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
			require.NoError(t, rerr)
			if json.Valid(data) && string(data) != "" && strings.Contains(string(data), "enabled") {
				require.NoError(t, os.WriteFile(filepath.Join(dir, e.Name()), []byte("{garbage"), 0o600))
			}
		}
	}
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "an unreadable strict record refuses launches")
}

func TestStrictStatus_ShapeAndExactFix(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	cfg, _ := Load(f.mtixDir)
	st := f.approvals.StrictStatus(cfg)
	require.True(t, st.Enabled)
	require.Len(t, st.Programs, 1)
	require.False(t, st.Programs[0].Approved)
	require.Equal(t, ProgramTrustFix("wake"), st.Programs[0].Fix)
	require.Contains(t, st.Fix, ProgramTrustFix("wake"))
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	require.ElementsMatch(t, []string{"enabled", "detail", "fix", "programs"}, keysOf(m))
	prog := m["programs"].([]any)[0].(map[string]any)
	require.ElementsMatch(t, []string{"hook", "program", "approved", "detail", "fix"}, keysOf(prog))
}

func TestProgramTrustFix_QuotesTheName(t *testing.T) {
	require.Equal(t, "mtix hooks trust --program -- 'wake'", ProgramTrustFix("wake"))
	require.Equal(t, "mtix hooks trust --program -- 'a'\"'\"'b'", ProgramTrustFix("a'b"))
}

// caseVariantFixture returns a project holding bin/tool and an upper-cased
// spelling of that file's absolute path. It skips on a case-sensitive volume,
// where that spelling names a different (absent) file.
func caseVariantFixture(t *testing.T) (prepareFixture, string) {
	t.Helper()
	f := newFixtureWithScript(t, "[cat, x]", "bin/tool", "#!/bin/sh\n")
	variant := strings.ToUpper(filepath.Join(f.root, "bin", "tool"))
	if _, err := os.Stat(variant); err != nil {
		t.Skip("case-sensitive volume: an upper-cased spelling names a different file")
	}
	return f, variant
}

func TestProjectPath_CaseVariantSpellingIsInsideTheProject(t *testing.T) {
	f, variant := caseVariantFixture(t)
	_, _, inside := projectPath(f.logical, f.root, variant)
	require.True(t, inside, "file identity decides, not the spelling")
	_, _, outside := projectPath(f.logical, f.root, "/bin/cat")
	require.False(t, outside)
}

func TestApproveProgram_CaseVariantPathToProjectFileIsRefused(t *testing.T) {
	f, variant := caseVariantFixture(t)
	yaml := "hooks:\n  - name: wake\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: " + execValue("["+variant+", x]") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "hooks.yaml"), []byte(yaml), 0o600))
	require.NoError(t, f.approvals.Approve("wake", true))
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.Error(t, err, "strict approval refuses a case-variant spelling of a project file")
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "launch refuses it too, with strict mode off")
}

func TestStrict_PinnedProgramIsRecordedInTheFiringAudit(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	program, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.Contains(t, plan.Audit(), "cat="+program, "the audit shows the path that ran")
}

func TestStrictStatus_UnreadableRecordHasATerminalFix(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	corruptStrictRecord(t, f)
	cfg, _ := Load(f.mtixDir)
	st := f.approvals.StrictStatus(cfg)
	require.False(t, st.Enabled)
	require.Contains(t, st.Fix, "mtix hooks strict on")
	require.NotContains(t, st.Fix, "status --json", "the fix is not circular")
}

func corruptStrictRecord(t *testing.T, f prepareFixture) {
	t.Helper()
	dir, err := f.state.Path()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	hit := 0
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, rerr)
		if strings.Contains(string(data), "enabled") {
			require.NoError(t, os.WriteFile(filepath.Join(dir, e.Name()), []byte("{garbage"), 0o600))
			hit++
		}
	}
	require.Equal(t, 1, hit)
}
