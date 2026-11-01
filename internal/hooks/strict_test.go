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
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
	target, err := filepath.EvalSymlinks(cat)
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "mycat")
	require.NoError(t, os.Symlink(target, link))
	f := strictFixture(t, "["+link+", data/x]")
	_, err = f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.Contains(t, plan.Audit(), link+"="+target, "the audit maps the written link to the path that ran")
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

func TestUnderRoot_DecidesByFileIdentityWithoutSkipping(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root := filepath.Join(base, "project")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin", "tool"), []byte("x"), 0o600))
	sibling := filepath.Join(base, "sibling")
	require.NoError(t, os.Mkdir(sibling, 0o700))
	for name, tc := range map[string]struct {
		path string
		want bool
	}{
		"root itself":                  {root, true},
		"existing file under root":     {filepath.Join(root, "bin", "tool"), true},
		"missing path under root":      {filepath.Join(root, "bin", "missing", "deeper"), true},
		"unclean path under root":      {filepath.Join(root, "bin") + "/../bin/tool", true},
		"sibling of root":              {sibling, false},
		"missing path in sibling":      {filepath.Join(sibling, "nope"), false},
		"parent of root (ancestor)":    {base, false},
		"filesystem root":              {string(filepath.Separator), false},
		"missing path beside the root": {filepath.Join(base, "projectX", "bin"), false},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, underRoot(root, tc.path))
		})
	}
	require.False(t, underRoot(filepath.Join(base, "absent-root"), root), "an unreadable root is never reported as containing anything")
}

// fakeEnv writes an executable named env outside every PATH directory that
// runs whatever follows it, as a real env does.
func fakeEnv(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "env")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexec \"$@\"\n"), 0o700)) //nolint:gosec // test launcher
	return path
}

func TestStrict_On_WrapperThatIsNotAKnownEnvNeedsProgramApproval(t *testing.T) {
	target, err := filepath.EvalSymlinks(fakeEnv(t))
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "env")
	require.NoError(t, os.Symlink(target, link))
	f := strictFixture(t, "["+link+", sh, scripts/run.sh]")
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "a program named env outside the PATH directories is not the known env")
	require.Contains(t, err.Error(), "strict program mode")
	program, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err, "the wrapper itself is what the operator approves")
	require.Equal(t, target, program, "the approval is bound to the symlink-resolved wrapper")
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.Equal(t, target, plan.Argv[0], "the wrapper slot is pinned to the resolved path")
	wrapped, err := resolveLauncher(f.root, "sh")
	require.NoError(t, err)
	require.Equal(t, wrapped, plan.Argv[1], "the wrapped program is left as launched, never overwritten by the pin")
	require.Contains(t, plan.Audit(), link+"="+target)
}

func TestStrict_On_KnownEnvInAPathDirectoryNeedsNoApproval(t *testing.T) {
	env, err := exec.LookPath("env")
	require.NoError(t, err)
	for _, command := range []string{"[env, sh, scripts/run.sh]", "[" + env + ", sh, scripts/run.sh]"} {
		t.Run(command, func(t *testing.T) {
			f := strictFixture(t, command)
			_, err := f.approvals.PrepareExec(f.hook(t))
			require.NoError(t, err)
		})
	}
}

func TestStrict_On_RelativeWrapperNamedEnvIsNotTrustedByName(t *testing.T) {
	f := strictFixture(t, "[../env, sh, scripts/run.sh]")
	_, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
}

// recordFilesContaining returns operator-state files whose body has marker.
func recordFilesContaining(t *testing.T, f prepareFixture, marker string) []string {
	t.Helper()
	dir, err := f.state.Path()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, rerr := os.ReadFile(path) //nolint:gosec // test reads its own state dir
		require.NoError(t, rerr)
		if strings.Contains(string(data), marker) {
			out = append(out, path)
		}
	}
	return out
}

func TestStrict_PermissiveStrictRecordFailsClosed(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	files := recordFilesContaining(t, f, `"enabled"`)
	require.Len(t, files, 1)
	require.NoError(t, os.Chmod(files[0], 0o666)) //nolint:gosec // test makes the record too permissive
	_, err := f.approvals.StrictEnabled()
	require.Error(t, err, "an operator-state report on the strict record is an error, never off")
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "launches are refused while the strict record cannot be trusted")
}

func TestStrict_UnreadableProgramRecordRefusesLaunch(t *testing.T) {
	for name, damage := range map[string]func(t *testing.T, path string){
		"corrupt":    func(t *testing.T, path string) { require.NoError(t, os.WriteFile(path, []byte("{garbage"), 0o600)) },
		"permissive": func(t *testing.T, path string) { require.NoError(t, os.Chmod(path, 0o666)) }, //nolint:gosec // test
	} {
		t.Run(name, func(t *testing.T) {
			f := strictFixture(t, "[cat, data/x]")
			_, err := f.approvals.ApproveProgram(f.hook(t), true)
			require.NoError(t, err)
			files := recordFilesContaining(t, f, `"program"`)
			require.Len(t, files, 1)
			damage(t, files[0])
			_, err = f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err)
			require.Contains(t, err.Error(), "cannot be read", "the refusal says the record is unusable, not merely absent")
		})
	}
}

// foreignHost rewrites a record so it claims to belong to another host, which
// the operator-state reader reports instead of returning data.
func foreignHost(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test reads its own state dir
	require.NoError(t, err)
	var rec map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &rec))
	rec["host_id"] = json.RawMessage(`"00000000000000000000000000000000"`)
	out, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))
}

func TestStrict_RecordFromAnotherHostFailsClosed(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	files := recordFilesContaining(t, f, `"enabled"`)
	require.Len(t, files, 1)
	foreignHost(t, files[0])
	_, err := f.approvals.StrictEnabled()
	require.Error(t, err, "a reported record is an error, never off")
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
}

func TestStrict_ProgramRecordFromAnotherHostIsUnreadableNotAbsent(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	files := recordFilesContaining(t, f, `"program"`)
	require.Len(t, files, 1)
	foreignHost(t, files[0])
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be read")
}

// linkedPathDir returns a directory holding one link to the PATH directory that
// contains the named program, so linkDir/name names the same file as the PATH
// entry but through a link.
func linkedPathDir(t *testing.T, name string) (linkDir, realDir string) {
	t.Helper()
	program, err := exec.LookPath(name)
	require.NoError(t, err)
	realDir, err = filepath.EvalSymlinks(filepath.Dir(program))
	require.NoError(t, err)
	linkDir = filepath.Join(t.TempDir(), "pathdir")
	require.NoError(t, os.Symlink(realDir, linkDir))
	return linkDir, realDir
}

// escapeChain returns a program path written through a link and parent-directory
// segments whose textual clean and physical location differ, with a program
// of that name placed at the physical location.
func escapeChain(t *testing.T, root, realDir, name string) string {
	t.Helper()
	climb := len(splitPathComponents(root)) + 1
	chain := filepath.Join(root, "planted")
	for i := 0; i < climb; i++ {
		chain = filepath.Join(chain, "k")
	}
	require.NoError(t, os.MkdirAll(chain, 0o700))
	require.NoError(t, os.Symlink(chain, filepath.Join(root, "lnk")))
	landing := filepath.Join(root, "planted")
	dir := filepath.Join(landing, filepath.FromSlash(realDir[1:]))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // test program
	text := root + "/lnk"
	for i := 0; i < climb; i++ {
		text += "/.."
	}
	return text + realDir + "/" + name
}

func TestStrict_KnownWrapperAndInterpreterAreJudgedByThePhysicalDirectory(t *testing.T) {
	for _, name := range []string{"env", "sh"} {
		linkDir, realDir := linkedPathDir(t, name)
		root, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		probe := escapeChain(t, root, realDir, name)
		known := knownEnvWrapper
		if name == "sh" {
			known = knownInterpreterProgram
		}
		require.False(t, known(root, probe), "a path that reads like a PATH location but lands elsewhere is not known: %s", name)
		require.True(t, known(root, filepath.Join(linkDir, name)), "a link to the PATH directory is the PATH directory: %s", name)
		require.True(t, known(root, name), "a bare name is looked up on the fixed PATH")
		require.True(t, known(root, filepath.Join(realDir, name)))
	}
}

func TestStrict_On_KnownEnvAndInterpreterSlotsArePinnedToThePhysicalDirectory(t *testing.T) {
	envLink, envReal := linkedPathDir(t, "env")
	shLink, shReal := linkedPathDir(t, "sh")
	command := "[" + filepath.Join(envLink, "env") + ", " + filepath.Join(shLink, "sh") + ", scripts/run.sh]"
	f := strictFixture(t, command)
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(envReal, "env"), plan.Argv[0], "the env slot is pinned")
	require.Equal(t, filepath.Join(shReal, "sh"), plan.Argv[1], "the interpreter slot is pinned")
	off := newPrepareFixture(t, command)
	plan, err = off.approvals.PrepareExec(off.hook(t))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(envLink, "env"), plan.Argv[0], "strict off leaves the launch as written")
}

// The known slots are pinned in every strict launch, including when another
// slot is the approved program.
func TestStrict_On_KnownWrapperBeforeAnApprovedProgramIsPinned(t *testing.T) {
	envLink, envReal := linkedPathDir(t, "env")
	f := strictFixture(t, "["+filepath.Join(envLink, "env")+", cat, data/x]")
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(envReal, "env"), plan.Argv[0], "the wrapper slot is pinned beside the approved program")
}

func TestStrict_On_KnownInterpreterBehindAnApprovedWrapperIsPinned(t *testing.T) {
	wrapper, err := filepath.EvalSymlinks(fakeEnv(t))
	require.NoError(t, err)
	shLink, shReal := linkedPathDir(t, "sh")
	f := strictFixture(t, "["+wrapper+", "+filepath.Join(shLink, "sh")+", scripts/run.sh]")
	_, err = f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.Equal(t, wrapper, plan.Argv[0])
	require.Equal(t, filepath.Join(shReal, "sh"), plan.Argv[1], "the interpreter slot is pinned beside the approved wrapper")
}

func TestStrict_ProgramRecordForAnotherProjectIsRefused(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	files := recordFilesContaining(t, f, `"program"`)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0]) //nolint:gosec // test reads its own state dir
	require.NoError(t, err)
	var rec struct {
		HostID string          `json:"host_id"`
		Data   json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(data, &rec))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Data, &body))
	body["project"] = "another-project"
	rec.Data, err = json.Marshal(body)
	require.NoError(t, err)
	out, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(files[0], out, 0o600))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "a record whose project field differs is not this project's approval")
}

func TestStrict_RecordsLiveOutsideTheProject(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	dir, err := f.state.Path()
	require.NoError(t, err)
	for _, marker := range []string{`"enabled"`, `"program"`} {
		for _, file := range recordFilesContaining(t, f, marker) {
			rel, relErr := filepath.Rel(f.root, file)
			require.NoError(t, relErr)
			require.True(t, strings.HasPrefix(rel, ".."), "%s is outside the project root", file)
			require.True(t, strings.HasPrefix(file, dir))
		}
	}
	require.Len(t, recordFilesContaining(t, f, `"enabled"`), 1)
}

func TestApproveProgramShowing_ShowsTheBoundProgramBeforeTheWrite(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	var shown string
	written := -1
	program, err := f.approvals.ApproveProgramShowing(f.hook(t), true, func(p string) error {
		shown = p
		written = len(recordFilesContaining(t, f, `"program"`))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 0, written, "the program is shown before anything is written")
	require.Equal(t, program, shown, "what is shown is what is bound")
	require.Len(t, recordFilesContaining(t, f, `"program"`), 1)
	_, err = f.approvals.ApproveProgramShowing(f.hook(t), false, func(string) error { t.Fatal("shown although refused"); return nil })
	require.Error(t, err)
}

// repointLink replaces the link at path with one to a directory that holds a
// planted program of the given name.
func repointLink(t *testing.T, path, name string) {
	t.Helper()
	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, name), []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // test program
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(other, path))
}

func TestStrict_On_WrapperChangedBetweenTheJudgementAndThePinIsRefused(t *testing.T) {
	envLink, _ := linkedPathDir(t, "env")
	f := strictFixture(t, "["+filepath.Join(envLink, "env")+", cat, data/x]")
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	f.approvals.afterJudge = func() { repointLink(t, envLink, "env") }
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "changed location")
}

func TestStrict_On_InterpreterChangedBetweenTheJudgementAndThePinIsRefused(t *testing.T) {
	wrapper, err := filepath.EvalSymlinks(fakeEnv(t))
	require.NoError(t, err)
	shLink, _ := linkedPathDir(t, "sh")
	f := strictFixture(t, "["+wrapper+", "+filepath.Join(shLink, "sh")+", scripts/run.sh]")
	_, err = f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	f.approvals.afterJudge = func() { repointLink(t, shLink, "sh") }
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "changed location")
}
