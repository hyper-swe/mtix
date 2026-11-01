// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// A name is bare only when it has no path separator on the running platform,
// the rule resolveLauncher uses (filepath.Base). A relative path is never
// trusted by its name. This test builds and runs on every platform.
func TestIsBareName_RelativeSpellingsAreNotBare(t *testing.T) {
	backslashIsSeparator := runtime.GOOS == "windows"
	for name, tc := range map[string]struct {
		program string
		bare    bool
	}{
		"plain name":                   {"env", true},
		"dot slash":                    {"./env", false},
		"parent slash":                 {"../env", false},
		"forward slash relative":       {"bin/env", false},
		"forward slash deeper":         {"a/b/sh", false},
		"backslash relative":           {`bin\env`, !backslashIsSeparator},
		"backslash dot":                {`.\env`, !backslashIsSeparator},
		"mixed slashes":                {`bin/sub\env`, false},
		"absolute is not a bare name":  {"/usr/bin/env", false},
		"trailing separator directory": {"env/", false},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.bare, isBareName(tc.program))
		})
	}
}

func TestKnownWrapperAndInterpreter_RelativeSpellingNamedLikeTheProgramIsNotKnown(t *testing.T) {
	root := t.TempDir()
	for _, program := range []string{"bin/env", "./env", "../env", "a/b/env"} {
		require.False(t, knownEnvWrapper(root, program), program)
	}
	for _, program := range []string{"bin/sh", "./sh", "../python3", "a/b/node"} {
		require.False(t, knownInterpreterProgram(root, program), program)
	}
	require.True(t, knownEnvWrapper(root, "env"))
	require.True(t, knownInterpreterProgram(root, "sh"))
}
