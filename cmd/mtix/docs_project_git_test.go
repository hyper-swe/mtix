// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Git policy tests keep MTIX.md local only when every existing pointer is ignored.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func briefingGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func TestRunDocsGenerate_PointerGitStatus_ControlsLocalExclusion(t *testing.T) {
	cases := []struct {
		name, ignore         string
		tracked, wantExclude bool
	}{
		{"all ignored", "/CLAUDE.md\n/AGENTS.md\n", false, true},
		{"one tracked", "/AGENTS.md\n", true, false},
		{"untracked default", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := projectBriefingDir(t)
			briefingGit(t, dir, "init", "-q")
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(tc.ignore), 0600))
			if tc.tracked {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("user\n"), 0600))
				briefingGit(t, dir, "add", "CLAUDE.md")
			}
			exclude := filepath.Join(dir, ".git", "info", "exclude")
			before := readProjectDoc(t, dir, ".gitignore")
			require.NoError(t, runDocsGenerate(false))
			require.NoError(t, runDocsGenerate(false))
			got := readProjectDoc(t, dir, ".git/info/exclude")
			require.Equal(t, tc.wantExclude, strings.Contains(got, "/MTIX.md\n"))
			require.LessOrEqual(t, strings.Count(got, "/MTIX.md\n"), 1)
			if tc.wantExclude {
				require.Contains(t, got, "# mtix: managed local briefing")
			}
			require.Equal(t, before, readProjectDoc(t, dir, ".gitignore"))
			require.FileExists(t, exclude)
		})
	}
}

func TestRunDocsGenerate_NoGit_DoesNotCreateExclude(t *testing.T) {
	dir := projectBriefingDir(t)
	require.NoError(t, runDocsGenerate(false))
	require.NoDirExists(t, filepath.Join(dir, ".git"))
}

func TestRunDocsGenerate_GitWorktree_UsesResolvedExclude(t *testing.T) {
	main := t.TempDir()
	briefingGit(t, main, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(main, ".gitignore"), []byte("/CLAUDE.md\n/AGENTS.md\n"), 0600))
	briefingGit(t, main, "add", ".gitignore")
	briefingGit(t, main, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	work := filepath.Join(t.TempDir(), "work")
	briefingGit(t, main, "worktree", "add", "-q", "-b", "fixture", work)
	t.Chdir(work)
	require.NoError(t, runDocsGenerate(false))
	path := briefingGit(t, work, "rev-parse", "--git-path", "info/exclude")
	if !filepath.IsAbs(path) {
		path = filepath.Join(work, path)
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "/MTIX.md\n")
}

func TestRunDocsGenerate_NestedProject_DoesNotChangeParentExclude(t *testing.T) {
	parent := t.TempDir()
	briefingGit(t, parent, "init", "-q")
	before := readProjectDoc(t, parent, ".git/info/exclude")
	require.NoError(t, os.WriteFile(filepath.Join(parent, ".gitignore"), []byte("CLAUDE.md\nAGENTS.md\n"), 0600))
	child := filepath.Join(parent, "project")
	require.NoError(t, os.Mkdir(child, 0700))
	t.Chdir(child)
	require.NoError(t, runDocsGenerate(false))
	require.Equal(t, before, readProjectDoc(t, parent, ".git/info/exclude"))
}

func TestRunDocsGenerate_CorruptGit_DefaultsToNoExcludeWrite(t *testing.T) {
	dir := projectBriefingDir(t)
	briefingGit(t, dir, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("CLAUDE.md\nAGENTS.md\n"), 0600))
	before := readProjectDoc(t, dir, ".git/info/exclude")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("invalid git head\n"), 0600))
	require.NoError(t, runDocsGenerate(false))
	require.Equal(t, before, readProjectDoc(t, dir, ".git/info/exclude"))
}

const ownedBriefingExclusion = "\n# mtix: managed local briefing\n/MTIX.md\n"

func TestRunDocsGenerate_ProjectExclusion_UnchangedPointersReconcileEveryRun(t *testing.T) {
	dir := projectBriefingDir(t)
	briefingGit(t, dir, "init", "-q")
	require.NoError(t, runDocsGenerate(false))
	original := "# user exclusion without final newline"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(original), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/CLAUDE.md\n/AGENTS.md\n"), 0600))
	require.NoError(t, runDocsGenerate(false))
	require.Equal(t, original+ownedBriefingExclusion, readProjectDoc(t, dir, ".git/info/exclude"))
	require.NoError(t, runDocsGenerate(false))
	require.Equal(t, original+ownedBriefingExclusion, readProjectDoc(t, dir, ".git/info/exclude"))
	briefingGit(t, dir, "add", "-f", "CLAUDE.md")
	require.NoError(t, runDocsGenerate(false))
	require.Equal(t, original, readProjectDoc(t, dir, ".git/info/exclude"))
}

func TestRunDocsGenerate_ProjectExclusion_UserRuleSurvivesWithNotice(t *testing.T) {
	dir := projectBriefingDir(t)
	briefingGit(t, dir, "init", "-q")
	require.NoError(t, runDocsGenerate(false))
	original := "# user bytes\r\n/MTIX.md"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(original+ownedBriefingExclusion), 0600))
	briefingGit(t, dir, "add", "CLAUDE.md")
	out := captureStdout(t, func() { require.NoError(t, runDocsGenerate(false)) })
	require.Equal(t, original, readProjectDoc(t, dir, ".git/info/exclude"))
	require.Equal(t, 1, strings.Count(out, "MTIX.md remains ignored by user Git rules"))
}

func TestRunDocsGenerate_ProjectExclusion_NoPointersRemovesOnlyOwnedBlock(t *testing.T) {
	cases := []string{"", "# user no final newline", "/MTIX.md\r\n# custom"}
	for _, original := range cases {
		t.Run(original, func(t *testing.T) {
			dir := projectBriefingDir(t)
			briefingGit(t, dir, "init", "-q")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "HYPERSWE.md"), []byte("hs"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(original+ownedBriefingExclusion), 0600))
			require.NoError(t, runDocsGenerate(false))
			require.Equal(t, original, readProjectDoc(t, dir, ".git/info/exclude"))
			require.NoFileExists(t, filepath.Join(dir, "CLAUDE.md"))
			require.NoFileExists(t, filepath.Join(dir, "AGENTS.md"))
			require.NoError(t, runDocsGenerate(false))
			require.Equal(t, original, readProjectDoc(t, dir, ".git/info/exclude"))
		})
	}
}
