// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		out[name] = value
	}
	return out
}

func TestChildEnvironment_LocaleWildcardNeverAdmitsCredentialShapedNames(t *testing.T) {
	parent := []string{"LC_API_TOKEN=t", "LC_DB_PASSWORD=p", "LC_MTIX_SYNC_DSN=d", "LC_FOO=x", "LC_ALL=C", "LC_CTYPE=C", "LC_TIME=C", "LC_MESSAGES=C", "LC_IDENTIFICATION=C"}
	got := envMap(childEnvironment(parent, "linux", Delivery{Hook: Hook{Name: "wake"}}, "", "/fixed"))
	for _, name := range []string{"LC_API_TOKEN", "LC_DB_PASSWORD", "LC_MTIX_SYNC_DSN", "LC_FOO"} {
		require.NotContains(t, got, name)
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LC_TIME", "LC_MESSAGES", "LC_IDENTIFICATION"} {
		require.Equal(t, "C", got[name], name)
	}
}

func TestInheritedByDefault_AppliesTheDenyListToEveryAdmittedName(t *testing.T) {
	for _, name := range []string{"LC_API_TOKEN", "LC_MTIX_SYNC_DSN", "LC_", "LC_BASH_ENV"} {
		require.False(t, inheritedByDefault("linux", name), name)
	}
	for _, name := range []string{"HOME", "LANG", "LC_ALL", "LC_PAPER", "LC_TELEPHONE", "TZ"} {
		require.True(t, inheritedByDefault("linux", name), name)
	}
	for _, name := range []string{"SystemRoot", "COMSPEC", "pathext", "UserProfile", "TEMP", "TMP"} {
		require.True(t, inheritedByDefault("windows", name), "documented Windows carve-out: "+name)
	}
	require.False(t, inheritedByDefault("linux", "ComSpec"))
}

func TestReviewProgram_AbsoluteInterpreterInADispatcherPathDirectoryIsOther(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/python3", []byte("#!/bin/sh\n"), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	review := reviewOf(t, "["+dir+"/python3, scripts/run.sh]")
	require.Equal(t, ClassOther, review.Class, "a directory only on the dispatcher PATH is not trusted by name")
}

func TestReviewProgram_BareProgramOnlyOnTheDispatcherPathIsNotResolved(t *testing.T) {
	dir, err := os.MkdirTemp("", "reviewpath")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(dir + "/onlyhere"); _ = os.Remove(dir) })
	require.NoError(t, os.WriteFile(dir+"/onlyhere", []byte("#!/bin/sh\n"), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	review := reviewOf(t, "[onlyhere, data/x]")
	require.Equal(t, ClassOther, review.Class)
	require.Equal(t, "onlyhere", review.Program, "resolution uses the fixed PATH, so the dispatcher copy is not reported")
}

func TestEnvPassProblem_DocumentedExampleNameIsAccepted(t *testing.T) {
	changelog, err := os.ReadFile("../../CHANGELOG.md")
	require.NoError(t, err)
	var note string
	for _, line := range strings.Split(string(changelog), "\n") {
		if strings.Contains(line, "(MTIX-112.10)") {
			note = line
		}
	}
	require.NotEmpty(t, note)
	require.Contains(t, note, "`MY_SETTING`", "the upgrade note names an example that env-pass accepts")
	require.Empty(t, envPassProblem("MY_SETTING"))
	require.NotEmpty(t, envPassProblem("NODE_ENV"), "the note says loader-family names are refused")
}

func TestChangelog_UpgradeNoteDoesNotDescribeThePriorExposure(t *testing.T) {
	changelog, err := os.ReadFile("../../CHANGELOG.md")
	require.NoError(t, err)
	require.NotContains(t, string(changelog), "Previously a hook also received")
}

func TestEnvPassCheck_BoundsTheListAndNameLength(t *testing.T) {
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("MY_SETTING_%d", i)
		}
		return out
	}
	hook := func(pass []string) Hook {
		return Hook{Name: "h", Exec: &ExecConfig{Command: []string{"sh"}, EnvPass: pass}}
	}
	require.Empty(t, envPassCheck(hook(names(32))))
	require.Contains(t, envPassCheck(hook(names(33))), "at most 32")
	require.Empty(t, envPassCheck(hook([]string{"A" + strings.Repeat("B", 127)})))
	require.Contains(t, envPassCheck(hook([]string{"A" + strings.Repeat("B", 128)})), "128")
}

func TestFixedPath_WindowsIgnoresTheDispatcherSystemRoot(t *testing.T) {
	t.Setenv("SystemRoot", `D:\evil`)
	t.Setenv("SYSTEMROOT", `D:\evil`)
	got := fixedPath(t.TempDir(), "", "windows")
	require.NotContains(t, got, "evil")
	require.Contains(t, got, `C:\Windows`)
}

func TestApprove_NamesTheEnvPassRefusalInsteadOfNotFound(t *testing.T) {
	f := newFixture(t, "{command: [sh, scripts/run.sh], env-pass: [GITHUB_TOKEN]}", fixtureOptions{unapproved: true})
	err := f.approvals.Approve("wake", true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "GITHUB_TOKEN")
	require.Contains(t, err.Error(), "credential")
	require.NotContains(t, err.Error(), "not found")
}
