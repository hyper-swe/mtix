// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-122: build cleanup is bounded, inspectable, and never recursive rm.
package mtix_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func recursiveRemove(source string) bool {
	source = strings.ReplaceAll(source, "\\\n", " ")
	command := regexp.MustCompile(`(?:^|[\s;])[-@+]*(?:/[^\s;]+/)?["']?rm["']?\s+([^\n;|&]+)`)
	for _, line := range strings.Split(source, "\n") {
		line = strings.SplitN(line, "#", 2)[0]
		for _, match := range command.FindAllStringSubmatch(line, -1) {
			for _, word := range strings.Fields(match[1]) {
				word = strings.Trim(word, `"'`)
				if word == "--" {
					break
				}
				if word == "--recursive" || (strings.HasPrefix(word, "-") && !strings.HasPrefix(word, "--") && strings.ContainsAny(word, "rR")) {
					return true
				}
			}
		}
	}
	return false
}

func TestBuildCleanup_RecursiveRemoveGuardVariants(t *testing.T) {
	cases := []struct {
		source    string
		forbidden bool
	}{
		{"rm -rf dist", true}, {"rm -fr dist", true}, {"rm -r -f dist", true},
		{"rm -R dist", true}, {"rm --recursive --force dist", true},
		{"rm \\\n -fR dist", true}, {"/bin/rm '-rf' dist", true}, {"command rm -r dist", true},
		{"trap 'rm -rf \"$dir\"' EXIT", true}, {"rm -f file", false},
		{"# rm -rf forbidden", false}, {"find dist -type f -print -delete", false},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) { require.Equal(t, tc.forbidden, recursiveRemove(tc.source)) })
	}
}

// Standard Make recipe prefixes must not hide a forbidden recursive remove.
func TestBuildCleanup_RecursiveRemoveGuardMakePrefixes(t *testing.T) {
	prefixes := []string{"@", "-", "+", "@-", "-@", "@+", "+@", "-+", "+-", "@-+", "@+-", "-@+", "-+@", "+@-", "+-@"}
	for _, prefix := range prefixes {
		t.Run(prefix, func(t *testing.T) {
			cases := []struct {
				source    string
				forbidden bool
			}{
				{"\t" + prefix + "rm -rf dist", true},
				{"  \t" + prefix + " /bin/rm '-fr' dist", true},
				{"\t" + prefix + "rm -r -f dist", true},
				{"\t" + prefix + "rm --recursive --force dist", true},
				{"\t" + prefix + "rm -f file", false},
				{"\t" + prefix + "rm --force file", false},
				{"\t" + prefix + "find dist -type f -print -delete", false},
			}
			for _, tc := range cases {
				require.Equal(t, tc.forbidden, recursiveRemove(tc.source), tc.source)
			}
		})
	}
}

func TestBuildCleanup_NoRecursiveRemoveInBuildScripts(t *testing.T) {
	root := projectRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, "scripts", "*.sh"))
	require.NoError(t, err)
	paths = append(paths, filepath.Join(root, "Makefile"))
	for _, path := range paths {
		source, err := os.ReadFile(path)
		require.NoError(t, err)
		require.False(t, recursiveRemove(string(source)), "%s contains recursive rm", filepath.Base(path))
	}
}

func cleanupFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "scripts"), 0700))
	for _, name := range []string{"clean-build-artifacts.sh", "build-agent-kit.sh"} {
		source, err := os.ReadFile(filepath.Join(projectRoot(t), "scripts", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", name), source, 0700))
	}
	return root
}

func runCleanup(t *testing.T, root string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(root, "scripts", "clean-build-artifacts.sh")}, args...)...)
	cmd.Dir = root
	return cmd.CombinedOutput()
}

func TestBuildCleanup_RemovesOnlyFixedArtifacts(t *testing.T) {
	root := cleanupFixture(t)
	for _, target := range []string{"internal/web/dist", "web/dist", "dist/mtix-agent-kit-v0.6.0-beta", "mtix", "cover.out"} {
		path := filepath.Join(root, target)
		if strings.Contains(target, "/") {
			require.NoError(t, os.MkdirAll(filepath.Join(path, "nested"), 0700))
			path = filepath.Join(path, "nested", "stale")
		}
		require.NoError(t, os.WriteFile(path, []byte("old"), 0600))
	}
	sentinel := filepath.Join(root, "keep")
	require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0600))
	targets := []string{"internal/web/dist", "web/dist", "dist/mtix-agent-kit-v0.6.0-beta", "mtix", "cover.out"}
	out, err := runCleanup(t, root, targets...)
	require.NoError(t, err, "%s", out)
	for _, target := range targets {
		_, err := os.Lstat(filepath.Join(root, target))
		require.True(t, os.IsNotExist(err), target)
	}
	out, err = runCleanup(t, root, targets...)
	require.NoError(t, err, "%s", out)
	data, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "keep", string(data))
}

func TestBuildCleanup_RejectsEscapesAndSymlinksBeforeDeleting(t *testing.T) {
	cases := []string{"../outside", "/tmp/outside", ".", "dist", "web", "dist/mtix-agent-kit-v../outside", "target-link", "parent-link", "nested-link"}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			root := cleanupFixture(t)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "keep")
			require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0600))
			target := tc
			switch tc {
			case "target-link":
				require.NoError(t, os.Mkdir(filepath.Join(root, "web"), 0700))
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "web/dist")))
				target = "web/dist"
			case "parent-link":
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "web")))
				target = "web/dist"
			case "nested-link":
				require.NoError(t, os.MkdirAll(filepath.Join(root, "web/dist"), 0700))
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "web/dist/link")))
				target = "web/dist"
				require.NoError(t, os.WriteFile(filepath.Join(root, "web/dist/stale"), []byte("stale"), 0600))
			}
			out, err := runCleanup(t, root, target)
			require.Error(t, err, "%s", out)
			data, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			require.Equal(t, "keep", string(data))
			if tc == "nested-link" {
				_, err := os.Stat(filepath.Join(root, "web/dist/stale"))
				require.NoError(t, err)
			}
		})
	}
}

func TestBuildCleanup_RejectsMakeOverridesBeforeBuild(t *testing.T) {
	for _, assignment := range []string{"EMBED_DIR=../outside", "WEB_DIR=../outside", "BINARY=../outside", "COVERFILE=../outside"} {
		t.Run(assignment, func(t *testing.T) {
			root := cleanupFixture(t)
			source, err := os.ReadFile(filepath.Join(projectRoot(t), "Makefile"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(root, "Makefile"), source, 0600))
			cmd := exec.Command("make", "build-web", assignment)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", out)
			require.Contains(t, string(out), "fixed build paths")
		})
	}
}

func TestBuildCleanup_AgentKitRejectsInvalidVersion(t *testing.T) {
	for _, version := range []string{"../outside", "0.6/../../outside", "", "..", "$(touch marker)"} {
		t.Run(version, func(t *testing.T) {
			root := cleanupFixture(t)
			cmd := exec.Command("bash", filepath.Join(root, "scripts/build-agent-kit.sh"), version)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", out)
			_, err = os.Stat(filepath.Join(root, "dist"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestBuildCleanup_ProbeTrapIsBoundedWithoutCredentials(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(projectRoot(t), "scripts/refresh-cloud-secrets.sh"))
	require.NoError(t, err)
	start := strings.Index(string(source), "cleanup_probe_dir() {")
	end := strings.Index(string(source)[start:], "\nPROBE_PARENT=") + start
	require.GreaterOrEqual(t, start, 0)
	require.Greater(t, end, start)
	function := string(source)[start:end]
	for _, scenario := range []string{"owned", "outside", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			parent := t.TempDir()
			outside := t.TempDir()
			probe := filepath.Join(parent, "mtix-cloud-probe.12345678")
			require.NoError(t, os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0600))
			switch scenario {
			case "owned":
				require.NoError(t, os.Mkdir(probe, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(probe, "main.go"), []byte("fixture"), 0600))
			case "outside":
				probe = outside
			case "symlink":
				require.NoError(t, os.Symlink(outside, probe))
			}
			cmd := exec.Command("bash", "-c", function+"\nPROBE_PARENT=$1; PROBE_DIR=$2; cleanup_probe_dir", "fixture", parent, probe)
			out, err := cmd.CombinedOutput()
			if scenario == "owned" {
				require.NoError(t, err, "%s", out)
				_, err = os.Stat(probe)
				require.True(t, os.IsNotExist(err))
			} else {
				require.Error(t, err, "%s", out)
			}
			data, err := os.ReadFile(filepath.Join(outside, "keep"))
			require.NoError(t, err)
			require.Equal(t, "keep", string(data))
		})
	}
}

func TestBuildCleanup_AgentKitRefusesOutputSymlink(t *testing.T) {
	for _, entry := range []string{"dist", "dist/mtix-agent-kit-v0.6.0", "dist/mtix-agent-kit-v0.6.0.tar.gz"} {
		t.Run(entry, func(t *testing.T) {
			root := cleanupFixture(t)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "keep")
			require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0600))
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, entry)), 0700))
			target := outside
			if strings.HasSuffix(entry, ".tar.gz") {
				target = sentinel
			}
			require.NoError(t, os.Symlink(target, filepath.Join(root, entry)))
			cmd := exec.Command("bash", filepath.Join(root, "scripts/build-agent-kit.sh"), "0.6.0")
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", out)
			data, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			require.Equal(t, "keep", string(data))
		})
	}
}
