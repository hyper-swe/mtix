// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests shipped wake routines with owned executable fixtures and captured input.
package docs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type wakeCapture struct{ dir string }

func wakeSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", path))
	require.NoError(t, err)
	return string(b)
}

func wakeVariant(t *testing.T, source, harness string) string {
	t.Helper()
	lines := strings.Split(source, "\n")
	selected := 0
	for i, line := range lines {
		if !wakeLaunchRE.MatchString(line) {
			continue
		}
		active := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if strings.Contains(active, harness+" ") {
			lines[i] = active
			selected++
		} else {
			lines[i] = "# " + active
		}
	}
	require.Equal(t, 1, selected, "documented launch variant")
	return strings.Join(lines, "\n")
}

func runWakeFixture(t *testing.T, source, input string) wakeCapture {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700))
	}
	write("input", input)
	write("mtix", "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$WAKE_DIR/mtix-args\"\ncat \"$WAKE_DIR/input\"\n")
	harness := "#!/bin/sh\nprintf 'called\\n' >> \"$WAKE_DIR/calls\"\nprintf '%s\\0' \"$@\" > \"$WAKE_DIR/args\"\ncat > \"$WAKE_DIR/stdin\"\n"
	for _, name := range []string{"claude", "codex", "agent"} {
		write(name, harness)
	}
	write("wake.sh", source)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", filepath.Join(dir, "wake.sh"), "fixture-worker")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "WAKE_DIR="+dir)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "owned routine output: %s", output)
	args, err := os.ReadFile(filepath.Join(dir, "mtix-args"))
	require.NoError(t, err)
	require.Equal(t, []string{"inbox", "--agent", "fixture-worker", "--format", "prompt"}, wakeArgs(args))
	_, err = os.Stat(filepath.Join(dir, "unexpected"))
	require.True(t, os.IsNotExist(err))
	return wakeCapture{dir}
}

func wakeArgs(b []byte) []string {
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

func assertWakeInput(t *testing.T, got wakeCapture, input, harness string) {
	t.Helper()
	args, err := os.ReadFile(filepath.Join(got.dir, "args"))
	require.NoError(t, err)
	want := []string{"-p"}
	if harness == "codex" {
		want = []string{"exec", "-"}
	}
	require.Equal(t, want, wakeArgs(args))
	stdin, err := os.ReadFile(filepath.Join(got.dir, "stdin"))
	require.NoError(t, err)
	require.Equal(t, strings.TrimRight(input, "\n")+"\n", string(stdin))
	calls, err := os.ReadFile(filepath.Join(got.dir, "calls"))
	require.NoError(t, err)
	require.Equal(t, "called\n", string(calls))
}

func TestWakeExample_Variants_ReceiveCanonicalInput(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	inputs := []string{"Ordinary input", "--fixture-mode\nnext line", "spaces 'single' and \"double\" quotes\n\nlast line\n\n", "literal $(touch unexpected) and `touch unexpected`; $HOME | & < >"}
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			variant := source
			if harness == "codex" {
				variant = wakeVariant(t, source, harness)
			}
			for _, input := range inputs {
				t.Run(input, func(t *testing.T) { assertWakeInput(t, runWakeFixture(t, variant, input), input, harness) })
			}
		})
	}
}

func TestWakeExample_EmptyInput_LaunchesNothing(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			variant := source
			if harness == "codex" {
				variant = wakeVariant(t, source, harness)
			}
			got := runWakeFixture(t, variant, "")
			for _, name := range []string{"calls", "args", "stdin"} {
				_, err := os.Stat(filepath.Join(got.dir, name))
				require.True(t, os.IsNotExist(err), name)
			}
		})
	}
}

func wakeManualCommands(text string) map[string][]string {
	commands := map[string][]string{}
	for _, snippet := range regexp.MustCompile("`([^`\\n]+)`").FindAllStringSubmatch(text, -1) {
		for _, harness := range []string{"claude", "codex", "agent"} {
			if wakeLaunchRE.MatchString(snippet[1]) && strings.Contains(snippet[1], harness+" ") && (strings.Contains(snippet[1], "|") || strings.Contains(snippet[1], "$")) {
				commands[harness] = append(commands[harness], snippet[1])
			}
		}
	}
	return commands
}

func TestWakeExample_ManualVariants_ReceiveCanonicalInput(t *testing.T) {
	commands := wakeManualCommands(wakeSource(t, "USERMANUAL.md"))
	require.Len(t, commands, 2)
	require.NotEmpty(t, commands["claude"])
	require.NotEmpty(t, commands["codex"])
	input := "--fixture-mode\nquotes ' and \"; literal $(touch unexpected)\n\nlast"
	for _, harness := range []string{"claude", "codex"} {
		for _, snippet := range commands[harness] {
			t.Run(harness, func(t *testing.T) {
				source := "set -eu\nPAYLOAD=$(mtix inbox --agent \"$1\" --format prompt)\n" + snippet + "\n"
				assertWakeInput(t, runWakeFixture(t, source, input), input, harness)
			})
		}
	}
}
