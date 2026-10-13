// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScriptOperand(t *testing.T) {
	cases := []struct {
		command []string
		want    int
	}{
		{[]string{"scripts/run.sh"}, 0},
		{[]string{"sh", "run.sh"}, 1},
		{[]string{"sh", "-e", "run.sh"}, 2},
		{[]string{"sh", "-ex", "run.sh"}, 2},
		{[]string{"sh", "+x", "run.sh"}, 2},
		{[]string{"sh", "+ex", "run.sh"}, 2},
		{[]string{"sh", "-o", "pipefail", "run.sh"}, 3},
		{[]string{"sh", "+o", "pipefail", "run.sh"}, 3},
		{[]string{"bash", "--norc", "--noprofile", "run.sh"}, 3},
		{[]string{"bash", "--", "run.sh"}, 2},
		{[]string{"env", "sh", "run.sh"}, 2},
		{[]string{"env", "-i", "A=b", "-u", "X", "sh", "run.sh"}, 6},
		{[]string{"env", "--", "sh", "run.sh"}, 3},
		{[]string{"env", "./tool"}, 1},
		{[]string{"/usr/bin/env", "python3", "-W", "ignore", "run.py"}, 4},
		{[]string{"python3.12", "-B", "run.py"}, 2},
		{[]string{"node", "--no-warnings", "run.js"}, 2},
		{[]string{"perl", "-w", "run.pl"}, 2},
		{[]string{"ruby", "-W2", "run.rb"}, 2},
		{[]string{"zsh", "run.zsh"}, 1},
		{[]string{"cat", "file"}, -1},
		{[]string{"sh"}, -1},
	}
	for _, c := range cases {
		got, err := scriptOperand(c.command)
		require.NoError(t, err, strings.Join(c.command, " "))
		require.Equal(t, c.want, got, strings.Join(c.command, " "))
	}
}

func TestScriptOperand_UnknownOptionRefusedByName(t *testing.T) {
	cases := []struct {
		command []string
		option  string
	}{
		{[]string{"bash", "--rcfile", "scripts/run.sh", "scripts/later.sh"}, "--rcfile"},
		{[]string{"sh", "--init-file", "x", "later.sh"}, "--init-file"},
		{[]string{"sh", "-c", "echo hi"}, "-c"},
		{[]string{"bash", "-ec", "echo hi"}, "-ec"},
		{[]string{"python3", "-c", "1"}, "-c"},
		{[]string{"python3", "-m", "pkg"}, "-m"},
		{[]string{"node", "-e", "1"}, "-e"},
		{[]string{"node", "-r", "mod", "run.js"}, "-r"},
		{[]string{"perl", "-e", "1"}, "-e"},
		{[]string{"ruby", "-e", "1"}, "-e"},
		{[]string{"env", "-P", "/bin", "sh", "later.sh"}, "-P"},
		{[]string{"env", "-S", "sh later.sh"}, "-S"},
		{[]string{"env", "-C", "/tmp", "sh", "x.sh"}, "-C"},
		{[]string{"env", "-a", "name", "sh", "x.sh"}, "-a"},
	}
	for _, c := range cases {
		_, err := scriptOperand(c.command)
		require.Error(t, err, strings.Join(c.command, " "))
		require.Contains(t, err.Error(), `"`+c.option+`"`, "the error names the option")
	}
}

func TestApprove_UnknownOptionRefused(t *testing.T) {
	for _, command := range []string{
		"[bash, --rcfile, scripts/run.sh, scripts/later.sh]",
		"[env, -P, /bin, sh, later.sh]",
		"[env, -S, 'sh later.sh']",
		"[sh, -c, echo hi]",
	} {
		t.Run(command, func(t *testing.T) {
			f := newFixture(t, command, fixtureOptions{unapproved: true})
			require.Error(t, f.approvals.Approve("wake", true), "the hook cannot be approved")
			decisions, _ := f.approvals.Check()
			require.False(t, decisions["wake"].Trusted)
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err)
			require.Nil(t, plan)
		})
	}
}

func TestPrepareExec_ScriptPositionAlwaysPinned(t *testing.T) {
	for _, command := range []string{
		"[scripts/later.sh]",
		"[sh, scripts/later.sh]",
		"[sh, -e, scripts/later.sh]",
		"[sh, +x, scripts/later.sh]",
		"[env, sh, scripts/later.sh]",
		"[env, -u, FOO, A=b, sh, later.sh]",
		"[sh, later.sh]",
		"[sh, scripts]",
	} {
		t.Run(command, func(t *testing.T) {
			f := newPrepareFixture(t, command)
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err, "a script operand with no approved file starts nothing")
			require.Nil(t, plan)
		})
	}
}

func TestPrepareExec_ScriptPositionApprovedFileRuns(t *testing.T) {
	for _, command := range []string{
		"[sh, -o, pipefail, scripts/run.sh]",
		"[sh, +x, scripts/run.sh]",
		"[env, A=b, sh, scripts/run.sh]",
	} {
		t.Run(command, func(t *testing.T) {
			f := newPrepareFixture(t, command)
			_, err := f.approvals.PrepareExec(f.hook(t))
			require.NoError(t, err)
		})
	}
}

func TestPrepareExec_NonScriptOperandsPassUnchanged(t *testing.T) {
	for _, operand := range []string{"https://example.invalid/x", "--config=conf/x.yaml", "missing/file.txt", "plainword", "scripts"} {
		t.Run(operand, func(t *testing.T) {
			f := newPrepareFixture(t, "[cat, "+operand+"]")
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.NoError(t, err)
			require.Equal(t, operand, plan.Argv[1])
		})
	}
}

func TestPrepareExec_OptionAndEnvValueFilesArePinned(t *testing.T) {
	for _, tc := range []struct{ command, prefix string }{
		{"[cat, --config=scripts/run.sh]", "--config="},
		{"[env, CONFIG=scripts/run.sh, cat]", "CONFIG="},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			f := newPrepareFixture(t, tc.command)
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.NoError(t, err)
			var pinned string
			for _, arg := range plan.Argv {
				if strings.HasPrefix(arg, tc.prefix) {
					pinned = strings.TrimPrefix(arg, tc.prefix)
				}
			}
			require.NotEmpty(t, pinned)
			require.NotEqual(t, f.script, pinned)
			require.FileExists(t, pinned)
			require.NoError(t, os.WriteFile(f.script, []byte("# edited\n"), 0o700))
			_, err = f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err, "an edited value file needs a new approval")
		})
	}
}

func TestPrepareExec_EnvValueNamingUnapprovedFileRefused(t *testing.T) {
	f := newPrepareFixture(t, "[env, CONFIG=data/new.txt, sh, scripts/run.sh]")
	require.NoError(t, os.Mkdir(filepath.Join(f.root, "data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "data", "new.txt"), []byte("new"), 0o600))
	_, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
}

func TestPrepareExec_ExistingUnapprovedOperandFileRefused(t *testing.T) {
	f := newPrepareFixture(t, "[cat, data/in.txt]")
	require.NoError(t, os.Mkdir(filepath.Join(f.root, "data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "data", "in.txt"), []byte("new"), 0o600))
	_, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
}

func TestPrepareExec_OperandLinkIntoProjectRefused(t *testing.T) {
	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(outside, "link.sh")
	f := newPrepareFixture(t, "[sh, "+link+"]")
	require.NoError(t, os.Symlink(f.script, link))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "a link from outside to an unpinned project file is refused")

	link2 := filepath.Join(outside, "link2.sh")
	g := newPrepareFixture(t, "[cat, "+link2+"]")
	require.NoError(t, os.Symlink(g.script, link2))
	_, err = g.approvals.PrepareExec(g.hook(t))
	require.Error(t, err, "also in a non-script position")
}

func TestPrepareExec_DanglingOperandLinkIntoProjectRefused(t *testing.T) {
	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(outside, "link.sh")
	f := newPrepareFixture(t, "[sh, "+link+"]")
	require.NoError(t, os.Symlink(filepath.Join(f.root, "scripts", "later.sh"), link))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "a link to a project file that does not exist yet is refused")
}

func TestPrepareExec_LogicalRootPathsAreProjectPaths(t *testing.T) {
	for _, command := range []string{
		`[sh, "{L}/scripts/later.sh"]`,
		`["{L}/scripts/later2.sh"]`,
	} {
		t.Run(command, func(t *testing.T) {
			f := newFixture(t, command, fixtureOptions{linked: true})
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err, "an unapproved script reached through the entered path is refused")
			require.Nil(t, plan)
		})
	}
}

func TestPrepareExec_LogicalRootApprovedScriptIsPinned(t *testing.T) {
	f := newFixture(t, `[sh, "{L}/scripts/run.sh"]`, fixtureOptions{linked: true})
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.NotEqual(t, filepath.Join(f.logical, "scripts", "run.sh"), plan.Argv[1])
	require.FileExists(t, plan.Argv[1])
}

func TestPrepareExec_PathLinkIntoProjectRefusedWithResolvedTarget(t *testing.T) {
	f := newPrepareFixture(t, "[tool]")
	bin := filepath.Join(f.root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	target := filepath.Join(bin, "tool")
	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Symlink(target, filepath.Join(outside, "tool")))
	t.Setenv("PATH", outside+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), target, "the message names the resolved target")
}

func TestPrepareExec_LinkedCopiesDirectoryRefused(t *testing.T) {
	f := newPrepareFixture(t, "[scripts/run.sh]")
	require.NoError(t, f.state.Ensure())
	opDir, err := f.state.Path()
	require.NoError(t, err)
	elsewhere, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(opDir, execCopiesDir)))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
	entries, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing was written through the link")
}

func TestPrepareExec_ScriptPathIsFirstScript(t *testing.T) {
	f := newPrepareFixtureWith(t, "[sh, scripts/run.sh, scripts/second.sh]", map[string]string{"scripts/second.sh": "# second\n"})
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.Equal(t, f.script, plan.ScriptPath)
}

func TestScriptOperand_NameVariantsAreInterpreters(t *testing.T) {
	cases := []struct {
		command []string
		want    int
	}{
		{[]string{"SH", "scripts/later.sh"}, 1},
		{[]string{"Bash", "scripts/later.sh"}, 1},
		{[]string{"bash5", "scripts/later.sh"}, 1},
		{[]string{"env", "SH", "scripts/later.sh"}, 2},
		{[]string{"perl5.34", "scripts/later.sh"}, 1},
		{[]string{"nodejs", "scripts/later.sh"}, 1},
		{[]string{"node22", "scripts/later.sh"}, 1},
		{[]string{"/usr/bin/Python3.12", "-B", "scripts/later.py"}, 2},
		{[]string{"ruby3.3", "scripts/later.rb"}, 1},
		{[]string{"ENV", "sh", "scripts/later.sh"}, 2},
	}
	for _, c := range cases {
		got, err := scriptOperand(c.command)
		require.NoError(t, err, strings.Join(c.command, " "))
		require.Equal(t, c.want, got, strings.Join(c.command, " "))
	}
	for _, command := range [][]string{
		{"ENV", "-S", "sh later.sh"},
		{"SH", "-c", "scripts/later.sh"},
		{"perl5.34", "-e", "1"},
		{"nodejs", "-e", "1"},
		{"/usr/bin/ENV", "-P", "/bin", "sh", "x.sh"},
	} {
		_, err := scriptOperand(command)
		require.Error(t, err, strings.Join(command, " "))
	}
}

func TestPrepareExec_NameVariantProbesRefused(t *testing.T) {
	for _, command := range []string{
		"[SH, scripts/later.sh]",
		"[Bash, scripts/later.sh]",
		"[env, SH, scripts/later.sh]",
	} {
		t.Run(command, func(t *testing.T) {
			f := newPrepareFixture(t, command)
			_, err := f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err, "an unapproved script behind a name variant starts nothing")
		})
	}
}

func TestPrepareExec_AssignmentShapeAtProgramOrScriptIsLiteralPath(t *testing.T) {
	for _, command := range []string{"[sh, X=scripts/run.sh]", "[X=scripts/run.sh]", "[sh, X=/usr/bin/true]"} {
		t.Run(command, func(t *testing.T) {
			f := newPrepareFixture(t, command)
			plan, err := f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err, "the argument is a path, not an assignment, and no such file was approved")
			require.Nil(t, plan)
		})
	}
}

func TestApprove_CodeLoadingEnvAssignmentRefused(t *testing.T) {
	for _, name := range []string{"BASH_ENV", "ENV", "NODE_OPTIONS", "PERL5OPT", "PERL5LIB", "RUBYOPT", "RUBYLIB", "PYTHONPATH", "PYTHONSTARTUP"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "[env, "+name+"=scripts/later.sh, bash, scripts/run.sh]", fixtureOptions{unapproved: true})
			err := f.approvals.Approve("wake", true)
			require.Error(t, err)
			require.Contains(t, err.Error(), name)
		})
	}
}

func TestExecAdapter_ScrubsCodeLoadingVariables(t *testing.T) {
	probe := "#!/bin/sh\nprintf '%s|%s|%s' \"${BASH_ENV-unset}\" \"${NODE_OPTIONS-unset}\" \"${KEEP-unset}\" > out.env\ncat > /dev/null\n"
	f := newPrepareFixtureWith(t, "[sh, scripts/envprobe.sh]", map[string]string{"scripts/envprobe.sh": probe})
	t.Setenv("BASH_ENV", filepath.Join(f.root, "startup.sh"))
	t.Setenv("NODE_OPTIONS", "--require x")
	t.Setenv("KEEP", "kept")
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.NoError(t, NewExecAdapter().Deliver(context.Background(), Delivery{Hook: f.hook(t), EventJSON: []byte(`{}`), Exec: plan}))
	require.Eventually(t, func() bool {
		data, readErr := os.ReadFile(filepath.Join(f.root, "out.env"))
		return readErr == nil && len(data) > 0
	}, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, "unset|unset|kept", readOut(t, f.root, "out.env"))
}

func TestResolveDeepest_FollowsChainsAndStopsOnCycles(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	target := filepath.Join(dir, "missing", "later.sh")
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "l2")))
	require.NoError(t, os.Symlink(filepath.Join(dir, "l2"), filepath.Join(dir, "l1")))
	require.Equal(t, target, resolveDeepest(filepath.Join(dir, "l1"), 0), "a chain of two dangling links is followed to its target")

	require.NoError(t, os.Symlink(filepath.Join(dir, "self"), filepath.Join(dir, "self")))
	done := make(chan string, 1)
	go func() { done <- resolveDeepest(filepath.Join(dir, "self"), 0) }()
	select {
	case got := <-done:
		require.NotEmpty(t, got)
	case <-time.After(5 * time.Second):
		t.Fatal("a self-referencing link must not loop")
	}
}

func TestPrepareExec_LinkChainIntoProjectRefused(t *testing.T) {
	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	l1 := filepath.Join(outside, "l1")
	f := newPrepareFixture(t, "[sh, "+l1+"]")
	require.NoError(t, os.Symlink(filepath.Join(outside, "l2"), l1))
	require.NoError(t, os.Symlink(filepath.Join(f.root, "scripts", "later.sh"), filepath.Join(outside, "l2")))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err)
}

func TestApprove_StartupRelocationVariablesRefused(t *testing.T) {
	for _, name := range []string{"ZDOTDIR", "PYTHONUSERBASE", "PYTHONHOME", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "[env, "+name+"=., sh, scripts/run.sh]", fixtureOptions{unapproved: true})
			err := f.approvals.Approve("wake", true)
			require.Error(t, err)
			require.Contains(t, err.Error(), name)
		})
	}
}

func TestApprove_HomeAssignmentInsideProjectRefused(t *testing.T) {
	for _, value := range []string{".", "scripts", "{ROOT}", "{ROOT}/scripts"} {
		t.Run(value, func(t *testing.T) {
			value = strings.ReplaceAll(value, "{ROOT}", "{L}")
			f := newFixture(t, `[env, "HOME=`+value+`", zsh, scripts/run.sh]`, fixtureOptions{unapproved: true})
			err := f.approvals.Approve("wake", true)
			require.Error(t, err)
			require.Contains(t, err.Error(), "HOME")
		})
	}
}

func TestApprove_HomeAssignmentOutsideProjectAllowed(t *testing.T) {
	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := newFixture(t, `[env, "HOME=`+outside+`", sh, scripts/run.sh]`, fixtureOptions{})
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
}

func TestExecAdapter_ScrubsStartupRelocationVariables(t *testing.T) {
	probe := "#!/bin/sh\nprintf '%s|%s|%s|%s' \"${ZDOTDIR-unset}\" \"${PYTHONUSERBASE-unset}\" \"${LD_PRELOAD-unset}\" \"${KEEP-unset}\" > out.env\ncat > /dev/null\n"
	f := newPrepareFixtureWith(t, "[sh, scripts/envprobe.sh]", map[string]string{"scripts/envprobe.sh": probe})
	t.Setenv("ZDOTDIR", f.root)
	t.Setenv("PYTHONUSERBASE", f.root)
	t.Setenv("LD_PRELOAD", filepath.Join(f.root, "x.so"))
	t.Setenv("KEEP", "kept")
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.NoError(t, NewExecAdapter().Deliver(context.Background(), Delivery{Hook: f.hook(t), EventJSON: []byte(`{}`), Exec: plan}))
	require.Eventually(t, func() bool {
		data, readErr := os.ReadFile(filepath.Join(f.root, "out.env"))
		return readErr == nil && len(data) > 0
	}, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, "unset|unset|unset|kept", readOut(t, f.root, "out.env"))
}

func TestScriptOperand_ZshOptionBEndsOptions(t *testing.T) {
	cases := []struct {
		command []string
		want    int
	}{
		{[]string{"zsh", "-f", "-b", "-e"}, 3},
		{[]string{"zsh", "-fb", "-e"}, 2},
		{[]string{"zsh", "-b", "run.zsh"}, 2},
		{[]string{"zsh", "-b"}, -1},
		{[]string{"/bin/Zsh5", "-b", "-x"}, 2},
		{[]string{"bash", "-b", "run.sh"}, 2},
		{[]string{"sh", "-b", "-e"}, 2},
		{[]string{"dash", "-b", "-e"}, 2},
	}
	for _, c := range cases {
		got, err := scriptOperand(c.command)
		require.NoError(t, err, strings.Join(c.command, " "))
		require.Equal(t, c.want, got, strings.Join(c.command, " "))
	}
}

func TestPrepareExec_ZshOptionBOperandIsScriptPosition(t *testing.T) {
	f := newPrepareFixture(t, "[zsh, -f, -b, -e]")
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "the operand after -b is the script and was never approved")
	require.Nil(t, plan)
}

func TestPrepareExec_AssignmentShapedProgramNameIsNotPinned(t *testing.T) {
	f := newPrepareFixtureWith(t, "[X=tool]", map[string]string{"tool": "#!/bin/sh\nexit 0\n"})
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "the program is a literal name, never rewritten to X=<copy>")
	require.Nil(t, plan)
}

func TestApprove_EveryEqualsArgumentBeforeProgramIsAnAssignment(t *testing.T) {
	for _, command := range []string{
		"[env, A.B=1, HOME=., zsh, scripts/run.sh]",
		"[env, A.B=1, ZDOTDIR=., zsh, scripts/run.sh]",
		`[env, "A%=1", sh, -c, ". ./evil.sh"]`,
		`[env, A.B=1, python3, -c, "exec(1)"]`,
		"[env, A.B=1, sh, scripts/later.sh]",
		"[env, =x, sh, scripts/run.sh]",
	} {
		t.Run(command, func(t *testing.T) {
			f := newFixture(t, command, fixtureOptions{unapproved: true})
			err := f.approvals.Approve("wake", true)
			require.Error(t, err)
			require.Contains(t, err.Error(), "identifier", "the refusal names the rule")
		})
	}
}

func TestApprove_EnvAssignmentAllowlist(t *testing.T) {
	for _, name := range []string{"LANG", "LC_ALL", "LC_TIME", "TZ", "TERM", "NO_COLOR", "MTIX_FLAG", "MY_SETTING", "WAKE_MODE2"} {
		t.Run("allowed "+name, func(t *testing.T) {
			f := newFixture(t, "[env, "+name+"=value, sh, scripts/run.sh]", fixtureOptions{})
			_, err := f.approvals.PrepareExec(f.hook(t))
			require.NoError(t, err)
		})
	}
	for _, name := range []string{
		"PERLLIB", "NODE_PATH", "RUBYGEMS_GEMDEPS", "SHELLOPTS", "BASHOPTS", "PS4", "PROMPT_COMMAND", "IFS",
		"GEM_PATH", "GEM_HOME", "PYTHONPYCACHEPREFIX", "__PYVENV_LAUNCHER__", "PYTHONEXECUTABLE", "LD_AUDIT",
		"GCONV_PATH", "DYLD_FRAMEWORK_PATH", "DYLD_FALLBACK_LIBRARY_PATH", "DYLD_FALLBACK_FRAMEWORK_PATH",
		"DYLD_VERSIONED_LIBRARY_PATH", "BASH_FUNC_x", "http_proxy", "my_setting", "ZSH_VERSION", "CDPATH",
	} {
		t.Run("refused "+name, func(t *testing.T) {
			f := newFixture(t, "[env, "+name+"=value, sh, scripts/run.sh]", fixtureOptions{unapproved: true})
			err := f.approvals.Approve("wake", true)
			require.Error(t, err)
			require.Contains(t, err.Error(), name, "the refusal names the variable")
			require.Contains(t, err.Error(), "allowlist", "and the rule")
		})
	}
}

func TestApprove_PathAssignmentRefused(t *testing.T) {
	for _, command := range []string{
		"[env, PATH=/nonexistent:bin:/usr/bin, sh, scripts/run.sh]",
		`[env, "PATH={L}/bin:/usr/bin:/bin", sh, scripts/run.sh]`,
	} {
		t.Run(command, func(t *testing.T) {
			f := newFixture(t, command, fixtureOptions{unapproved: true})
			err := f.approvals.Approve("wake", true)
			require.Error(t, err)
			require.Contains(t, err.Error(), "scrubbed environment", "PATH is refused by its own rule")
		})
	}
}

func TestPrepareExec_ProgramAfterEnvIsResolvedAndCheckedLikeTheProgram(t *testing.T) {
	f := newPrepareFixture(t, "[env, sh, scripts/run.sh]")
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(plan.Argv[1]), "the program after env is made absolute")

	bin := filepath.Join(f.root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "an interpreter that resolves into the project is refused")
}

func TestApprove_HomeThroughLinkIntoProjectRefused(t *testing.T) {
	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(outside, "home")
	f := newFixture(t, `[env, "HOME=`+link+`", zsh, scripts/run.sh]`, fixtureOptions{unapproved: true})
	require.NoError(t, os.Symlink(f.root, link))
	err = f.approvals.Approve("wake", true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "HOME")
}

func TestScriptOperand_PythonPycachePrefixRefused(t *testing.T) {
	_, err := scriptOperand([]string{"python3", "-X", "pycache_prefix=.", "scripts/run.py"})
	require.Error(t, err)
	got, err := scriptOperand([]string{"python3", "-X", "utf8", "scripts/run.py"})
	require.NoError(t, err)
	require.Equal(t, 3, got)
}
