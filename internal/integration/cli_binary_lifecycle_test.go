// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Shared binary lifetime and exit cleanup are observed from a separate process.
package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltBinaryLifecycle_ProcessExit_CleansOwnedDirectory(t *testing.T) {
	cases := []struct {
		name, mode string
		exit       int
	}{{"success", "pass", 0}, {"failed test", "fail", 1}, {"failed build", "buildfail", 0}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox := t.TempDir()
			tmp := filepath.Join(sandbox, "temp")
			require.NoError(t, os.Mkdir(tmp, 0700))
			executable, err := os.Executable()
			require.NoError(t, err)
			env := lifecycleChildEnvironment(t, sandbox, tmp, tc.mode)
			cmd := exec.Command(executable, "-test.run=^TestBuiltBinaryLifecycle_Child$", "-test.v")
			cmd.Env = env
			output, runErr := cmd.CombinedOutput()
			exit := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				require.ErrorAs(t, runErr, &exitErr, "%s", output)
				exit = exitErr.ExitCode()
			}
			assert.Equal(t, tc.exit, exit, "%s", output)
			entries, readErr := os.ReadDir(tmp)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "child exit must clean owned binary: %s", output)
			calls, readErr := os.ReadFile(filepath.Join(sandbox, "build-calls"))
			require.NoError(t, readErr)
			assert.Equal(t, "build\n", string(calls), "sync.Once builds once for both tests")
			if tc.mode == "buildfail" {
				assert.Contains(t, string(output), "failed to build mtix binary")
			}
		})
	}
}

func lifecycleChildEnvironment(t *testing.T, sandbox, tmp, mode string) []string {
	t.Helper()
	goPath, err := exec.LookPath("go")
	require.NoError(t, err)
	bin := filepath.Join(sandbox, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	action := "exec \"" + goPath + "\" \"$@\"\n"
	if mode == "buildfail" {
		action = "exit 23\n"
	}
	shim := "#!/bin/sh\nprintf 'build\\n' >> \"$MTIX_BINARY_BUILD_CALLS\"\n" + action
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(shim), 0700))
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TMPDIR=") && !strings.HasPrefix(value, "PATH=") && !strings.HasPrefix(value, "MTIX_BINARY_CHILD=") {
			env = append(env, value)
		}
	}
	return append(env, "TMPDIR="+tmp, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "MTIX_BINARY_CHILD="+mode, "MTIX_BINARY_BUILD_CALLS="+filepath.Join(sandbox, "build-calls"))
}

func TestBuiltBinaryLifecycle_Child(t *testing.T) {
	mode := os.Getenv("MTIX_BINARY_CHILD")
	if mode == "" {
		return
	}
	if strings.HasPrefix(mode, "refuse") {
		mtixBinaryPath = os.Getenv("MTIX_BINARY_REFUSED_PATH")
		if mode == "refusefail" {
			t.Error("intentional failure with refused cleanup")
		}
		return
	}
	var first string
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			binary := buildMtixBinary(t)
			if first == "" {
				first = binary
			} else {
				require.Equal(t, first, binary)
			}
			output, err := exec.Command(binary, "--version").CombinedOutput()
			require.NoError(t, err, "%s", output)
			t.Cleanup(func() { assert.FileExists(t, binary, "shared binary survives each test's cleanup") })
		})
	}
	if mode == "fail" {
		t.Errorf("intentional child failure after shared build: %s", first)
	}
}

func TestBuiltBinaryLifecycle_RefusedCleanup_PreservesExitAndSentinel(t *testing.T) {
	for _, tc := range []struct {
		mode string
		exit int
	}{{"refusepass", 0}, {"refusefail", 1}} {
		t.Run(tc.mode, func(t *testing.T) {
			sandbox := t.TempDir()
			tmp := filepath.Join(sandbox, "temp")
			require.NoError(t, os.Mkdir(tmp, 0700))
			outside := filepath.Join(sandbox, "mtix-cli-test-outside")
			require.NoError(t, os.Mkdir(outside, 0700))
			sentinel := filepath.Join(outside, "mtix")
			require.NoError(t, os.WriteFile(sentinel, []byte("do not remove"), 0600))
			executable, err := os.Executable()
			require.NoError(t, err)
			cmd := exec.Command(executable, "-test.run=^TestBuiltBinaryLifecycle_Child$", "-test.v")
			cmd.Env = append(lifecycleChildEnvironment(t, sandbox, tmp, tc.mode), "MTIX_BINARY_REFUSED_PATH="+sentinel)
			output, runErr := cmd.CombinedOutput()
			exit := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				require.ErrorAs(t, runErr, &exitErr, "%s", output)
				exit = exitErr.ExitCode()
			}
			assert.Equal(t, tc.exit, exit, "cleanup reporting must preserve original m.Run code")
			assert.Contains(t, string(output), "clean up shared CLI binary: refuse binary outside direct temp child")
			content, readErr := os.ReadFile(sentinel)
			require.NoError(t, readErr)
			assert.Equal(t, "do not remove", string(content))
		})
	}
}
