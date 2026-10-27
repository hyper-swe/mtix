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

// hostileEnvironment sets a dispatcher environment full of things a hook child
// must never see.
func hostileEnvironment(t *testing.T, root string) {
	t.Helper()
	for k, v := range map[string]string{
		"MTIX_SYNC_DSN":         "postgres://user:secret@host/db",
		"MTIX_AUTHOR_ID":        "sender-agent",
		"MTIX_RELAY_KEY":        "relay-secret",
		"AWS_SECRET_ACCESS_KEY": "aws-secret",
		"GITHUB_TOKEN":          "gh-token",
		"OPENAI_API_KEY":        "sk-test",
		"DB_PASSWORD":           "pw",
		"BASH_ENV":              filepath.Join(root, "evil.sh"),
		"HTTP_PROXY":            "http://proxy",
		"NODE_ENV":              "production",
		"MY_SETTING":            "data",
		"LC_ALL":                "C",
		"LANG":                  "C",
		"TZ":                    "UTC",
		"TERM":                  "dumb",
		"NO_COLOR":              "1",
		"PATH":                  "relative/bin:./bin:" + filepath.Join(root, "bin") + ":/usr/bin:/bin",
	} {
		t.Setenv(k, v)
	}
}

// childEnvironmentOf runs the hook through PrepareExec and Deliver and returns
// the environment the child saw.
func childEnvironmentOf(t *testing.T, f prepareFixture, hook Hook) map[string]string {
	t.Helper()
	out := filepath.Join(f.root, "child.env")
	plan, err := f.approvals.PrepareExec(hook)
	require.NoError(t, err)
	require.NoError(t, NewExecAdapter().Deliver(context.Background(), Delivery{Hook: hook, EventJSON: []byte(`{"e":1}`), Exec: plan}))
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(out)
		return err == nil && strings.Contains(string(data), "END")
	}, 10*time.Second, 20*time.Millisecond)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	env := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok && name != "" {
			env[name] = value
		}
	}
	return env
}

const envDumpScript = "#!/bin/sh\nenv > child.env\necho END >> child.env\ncat > /dev/null\n"

func envFixture(t *testing.T, command string) prepareFixture {
	t.Helper()
	return newFixtureWithScript(t, command, "scripts/envdump.sh", envDumpScript)
}

func TestExecChild_OnlyAllowlistedVariablesAreInherited(t *testing.T) {
	f := envFixture(t, "[sh, scripts/envdump.sh]")
	hostileEnvironment(t, f.root)
	env := childEnvironmentOf(t, f, f.hook(t))
	for _, name := range []string{"MTIX_SYNC_DSN", "MTIX_RELAY_KEY", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "OPENAI_API_KEY", "DB_PASSWORD", "BASH_ENV", "HTTP_PROXY", "NODE_ENV", "MY_SETTING"} {
		require.NotContains(t, env, name)
	}
	for name, want := range map[string]string{"LC_ALL": "C", "LANG": "C", "TZ": "UTC", "TERM": "dumb", "NO_COLOR": "1"} {
		require.Equal(t, want, env[name], name)
	}
	require.Contains(t, env, "HOME")
	require.NotEmpty(t, env["MTIX_EVENT"])
	require.Equal(t, "wake", env["MTIX_HOOK_ORIGIN"])
	require.Equal(t, filepath.Join(f.root, "scripts", "envdump.sh"), env["MTIX_HOOK_SCRIPT_PATH"])
}

func TestExecChild_AuthorIDIsTheHookAddresseeOrUnset(t *testing.T) {
	f := envFixture(t, "[sh, scripts/envdump.sh]")
	hostileEnvironment(t, f.root)
	hook := f.hook(t)
	env := childEnvironmentOf(t, f, hook)
	require.NotContains(t, env, "MTIX_AUTHOR_ID", "the dispatcher author id never reaches the child; no to-agent leaves it unset")

	g := newFixture(t, "[sh, scripts/envdump.sh]", fixtureOptions{extra: map[string]string{"scripts/envdump.sh": envDumpScript}, toAgent: "developer"})
	hostileEnvironment(t, g.root)
	env = childEnvironmentOf(t, g, g.hook(t))
	require.Equal(t, "developer", env["MTIX_AUTHOR_ID"])
}

func TestExecChild_PathIsFixedAndHasNoRelativeOrProjectEntry(t *testing.T) {
	f := envFixture(t, "[sh, scripts/envdump.sh]")
	hostileEnvironment(t, f.root)
	env := childEnvironmentOf(t, f, f.hook(t))
	require.Equal(t, FixedHookPath(f.root), env["PATH"])
	require.NotEmpty(t, env["PATH"])
	for _, dir := range filepath.SplitList(env["PATH"]) {
		require.True(t, filepath.IsAbs(dir), "no relative entry: %q", dir)
		require.False(t, dir == f.root || strings.HasPrefix(dir, f.root+string(filepath.Separator)), "no project entry: %q", dir)
	}
}

func TestFixedHookPath_ExcludesAMtixDirectoryInsideTheProject(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "bin")
	require.NotContains(t, filepath.SplitList(fixedPath(root, filepath.Join(inside, "mtix"), "linux")), inside)
	outside := t.TempDir()
	got := filepath.SplitList(fixedPath(root, filepath.Join(outside, "mtix"), "linux"))
	require.Equal(t, outside, got[0], "the running mtix binary directory comes first")
	require.Contains(t, got, "/usr/bin")
	darwin := filepath.SplitList(fixedPath(root, filepath.Join(outside, "mtix"), "darwin"))
	require.Contains(t, darwin, "/opt/homebrew/bin")
	require.NotContains(t, got, "/opt/homebrew/bin")
}

func TestPrepareExec_ResolvesProgramsAgainstTheFixedPathNotTheDispatchers(t *testing.T) {
	f := newPrepareFixture(t, "[tool]")
	bin := filepath.Join(f.root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tool"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "a program found only on the dispatcher PATH is not found")

	g := newPrepareFixture(t, "[env, sh, scripts/run.sh]")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	plan, err := g.approvals.PrepareExec(g.hook(t))
	require.NoError(t, err)
	for _, arg := range plan.Argv[:2] {
		require.False(t, strings.HasPrefix(arg, bin), "%q must not come from the dispatcher PATH", arg)
	}
}

func TestExecEnvPass_NamedVariablesReachTheChildExceptDeniedOnes(t *testing.T) {
	f := newFixtureWithScript(t, "{command: [sh, scripts/envdump.sh], env-pass: [MY_SETTING, LANG]}", "scripts/envdump.sh", envDumpScript)
	hostileEnvironment(t, f.root)
	env := childEnvironmentOf(t, f, f.hook(t))
	require.Equal(t, "data", env["MY_SETTING"])
	require.NotContains(t, env, "NODE_ENV", "NODE_ENV is in a refused loader family and was not named")
	require.Equal(t, "C", env["LANG"])
	for _, name := range []string{"GITHUB_TOKEN", "MTIX_SYNC_DSN", "BASH_ENV"} {
		require.NotContains(t, env, name, "not named, so not passed")
	}
}

func TestChildEnvironment_DenyListWinsOverEnvPass(t *testing.T) {
	hook := Hook{Name: "wake", Exec: &ExecConfig{Command: []string{"sh"}, EnvPass: []string{"GITHUB_TOKEN", "MTIX_SYNC_DSN", "PATH", "BASH_ENV", "MY_SETTING", "lowercase_ok"}}}
	parent := []string{"GITHUB_TOKEN=t", "MTIX_SYNC_DSN=d", "PATH=/evil", "BASH_ENV=/e", "MY_SETTING=v", "lowercase_ok=1", "OTHER=x", "HOME=/h"}
	env := childEnvironment(parent, "linux", Delivery{Hook: hook, EventJSON: []byte("{}")}, "/s", "/fixed")
	got := map[string]string{}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		got[name] = value
	}
	require.Equal(t, "v", got["MY_SETTING"])
	require.Equal(t, "1", got["lowercase_ok"])
	require.Equal(t, "/h", got["HOME"])
	require.Equal(t, "/fixed", got["PATH"], "PATH is never passable")
	for _, name := range []string{"GITHUB_TOKEN", "MTIX_SYNC_DSN", "BASH_ENV", "OTHER"} {
		require.NotContains(t, got, name)
	}
}

func TestChildEnvironment_WindowsAllowlistAndCaseInsensitiveNames(t *testing.T) {
	hook := Hook{Name: "wake"}
	parent := []string{"SYSTEMROOT=C:\\Windows", "ComSpec=cmd", "pathext=.EXE", "USERPROFILE=u", "TEMP=t", "TMP=t", "APPDATA=a", "SECRET_X=1", "Path=p"}
	env := childEnvironment(parent, "windows", Delivery{Hook: hook}, "", "C:\\fixed")
	got := map[string]bool{}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		got[strings.ToUpper(name)] = true
	}
	for _, name := range []string{"SYSTEMROOT", "COMSPEC", "PATHEXT", "USERPROFILE", "TEMP", "TMP", "PATH"} {
		require.True(t, got[name], name)
	}
	for _, name := range []string{"APPDATA", "SECRET_X"} {
		require.False(t, got[name], name)
	}
	count := 0
	for _, kv := range env {
		if strings.EqualFold(strings.SplitN(kv, "=", 2)[0], "PATH") {
			count++
		}
	}
	require.Equal(t, 1, count, "the inherited Path is replaced, not duplicated")
}

func TestExecEnvPass_DeniedOrMalformedNamesDisableTheHook(t *testing.T) {
	for _, name := range []string{"MTIX_SYNC_DSN", "MTIX_ANYTHING", "PATH", "BASH_ENV", "LD_PRELOAD", "PYTHONSTARTUP", "GITHUB_TOKEN", "DB_PASSWORD", "AWS_SECRET_ACCESS_KEY", "DATABASE_DSN", "API_KEY", "SERVICE_KEY", "1BAD", "BAD-NAME", "A=B", ""} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			yaml := "hooks:\n  - name: wake\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [sh, x.sh], env-pass: [\"" + name + "\"]}\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(yaml), 0o600))
			cfg, warnings := Load(dir)
			require.Empty(t, cfg.Hooks, "the hook is disabled")
			require.Len(t, warnings, 1)
			require.Contains(t, warnings[0], "env-pass")
		})
	}
}

func TestExecEnvPass_IsPartOfTheApprovedStanza(t *testing.T) {
	f := newFixtureWithScript(t, "{command: [sh, scripts/envdump.sh], env-pass: [MY_SETTING]}", "scripts/envdump.sh", envDumpScript)
	_, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err)
	edited := f.hook(t)
	edited.Exec.EnvPass = []string{"MY_SETTING", "OTHER_SETTING"}
	_, err = f.approvals.PrepareExec(edited)
	require.Error(t, err, "adding a pass-through name needs a new approval")
}

func TestChildEnvironment_AuthorIDOnlyForAValidAddressee(t *testing.T) {
	has := func(env []string) bool {
		for _, kv := range env {
			if strings.HasPrefix(kv, "MTIX_AUTHOR_ID=") {
				return true
			}
		}
		return false
	}
	for to, want := range map[string]bool{"developer": true, "": false, strings.Repeat("a", 65): false, "bad\nid": false, strings.Repeat("a", 64): true} {
		hook := Hook{Name: "wake", Match: Match{ToAgent: to}}
		require.Equal(t, want, has(childEnvironment([]string{"MTIX_AUTHOR_ID=sender"}, "linux", Delivery{Hook: hook}, "", "/fixed")), "%q", to)
	}
}

func TestLookPathFixed_IgnoresRelativeDirectoriesAndNonExecutables(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "tool")
	require.NoError(t, os.WriteFile(tool, []byte("x"), 0o600))
	_, found := lookPathFixed([]string{dir}, "tool")
	require.False(t, found, "not executable")
	require.NoError(t, os.Chmod(tool, 0o700))
	got, found := lookPathFixed([]string{dir}, "tool")
	require.True(t, found)
	require.Equal(t, tool, got)
	t.Chdir(dir)
	_, found = lookPathFixed([]string{"."}, "tool")
	require.False(t, found, "a relative directory is never searched")
	_, found = lookPathFixed([]string{""}, "tool")
	require.False(t, found)
}
