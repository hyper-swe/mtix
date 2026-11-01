// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Markup tests distinguish prose references from complete executable examples.
package docs

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// wakeDerivedProblems gives command fragments explicit input provenance rather
// than inferring their role from a variable name. The consumer stays unchanged.
func wakeDerivedProblems(text string, prefix ...string) []string {
	var captures strings.Builder
	seen := map[string]bool{}
	for _, variable := range wakeVariableRE.FindAllStringSubmatch(text, -1) {
		name := variable[1]
		if seen[name] || name == "AGENT" {
			continue
		}
		seen[name] = true
		fmt.Fprintf(&captures, "%s=\"$(mtix inbox --agent worker --format prompt)\"\n", name)
	}
	return wakeLaunchProblems(strings.Join(prefix, "") + captures.String() + text)
}

func TestWakeGuidance_ProseAndReferences_Accepted(t *testing.T) {
	for _, text := range []string{
		"Keep `$PAYLOAD` quoted", `# Keep "$PAYLOAD" quoted.`,
		`retain "$PAYLOAD" as text`, "retain `$PAYLOAD` as text",
		"Retain `$PAYLOAD` as text.", "the stored `$FRESH` remains quoted",
		`Courier "$PAYLOAD" finished`, "Courier `$PAYLOAD` batch",
		"Run `mtix hooks status` or `mtix hooks status --json` to validate the operator-local state directory and read corrective guidance. This check creates no state and grants no hook approval. The directory is `$XDG_CONFIG_HOME/mtix` (default `~/.config/mtix`) on macOS and Linux, or `%APPDATA%\\mtix` on Windows. Use an operator-owned directory with mode 0700 and files with mode 0600; Windows requires a protected user-only ACL. Keep it outside projects, git work trees, temporary directories, `~/.codex`, `CODEX_HOME` and harness writable roots, with no symlink components. Never commit or dotfile-sync it.",
	} {
		t.Run(text, func(t *testing.T) { require.Empty(t, wakeLaunchProblems(text, true)) })
	}
}

func TestWakeGuidance_ExecutableContexts_PreserveOperands(t *testing.T) {
	forms := []string{
		`Courier "$PAYLOAD" finished`, `courier "$PAYLOAD" finished`,
		"Courier `$PAYLOAD` batch", "Courier `${PAYLOAD}`", `Router "$PAYLOAD" batch`,
		`retain "$PAYLOAD" as text`, `mtix inbox --agent w --format prompt | xargs -0 gemini -p`,
		"`printf '%s\\n' \"$PAYLOAD\" | claude -p` >result",
		"`printf '%s\\n' \"$PAYLOAD\" | claude -p` | relay",
		"`printf '%s\\n' \"$PAYLOAD\" | claude -p` \"$PAYLOAD\"",
		"remote-worker flavor fast `printf '%s\\n' \"$PAYLOAD\" | claude -p`",
	}
	for i, form := range forms {
		for j, frame := range []string{"```sh\n%s\n```", "Use `` %s ``.", "~~~bash\n# %s\n~~~"} {
			t.Run(fmt.Sprintf("%d/%d", i, j), func(t *testing.T) {
				require.NotEmpty(t, wakeLaunchProblems(fmt.Sprintf(frame, form), true))
			})
		}
	}
}

func TestWakeGuidance_CapturedValues_RetainIdentity(t *testing.T) {
	for _, command := range []string{
		`git add -- "$FRESH"`, `Courier "$FRESH" finished`, "Courier `$FRESH` batch",
		`mtix inbox --agent "$FRESH" --format prompt`,
		`OTHER="$(mtix inbox --agent "$FRESH" --format prompt)"`,
		`FIRST="$FRESH"; SECOND="$FIRST"; courier "$SECOND"`,
	} {
		t.Run(command, func(t *testing.T) {
			text := "```sh\n" + `FRESH="$(mtix inbox --agent worker --format prompt)"` + "\n" + command + "\n```"
			require.NotEmpty(t, wakeLaunchProblems(text, true))
		})
	}
}

func TestWakeGuidance_UnrelatedValues_Accepted(t *testing.T) {
	for _, command := range []string{
		`NOW=$(date -u)`, `mtix sync backup --output "/tmp/mtix-${STAMP}.sql"`,
		"NOW=`date -u`", "NOW=`env -i date -u`",
		`git add -- "$UNRELATED_FILE"`, `printf '%s\n' "$UNRELATED_TEXT" | sort`,
	} {
		t.Run(command, func(t *testing.T) {
			require.Empty(t, wakeLaunchProblems("```sh\n"+command+"\n```", true))
		})
	}
}

func TestWakeGuidance_SourcePipeline_Checked(t *testing.T) {
	for _, source := range []string{
		"mtix inbox --agent w --format prompt", "env mtix inbox --agent w --format prompt",
		"command -- mtix inbox --format context", "mtix inbox --format json",
	} {
		t.Run(source, func(t *testing.T) {
			require.NotEmpty(t, wakeLaunchProblems("```sh\n"+source+" | xargs -0 gemini -p\n```", true))
		})
	}
}

func TestWakeGuidance_SourceCommandBoundaries_Checked(t *testing.T) {
	for _, command := range []string{
		`mtix inbox|xargs -0 next-runtime`,
		`/usr/local/bin/mtix inbox --format prompt | next-runtime`,
		`env -- mtix inbox --format prompt | next-runtime`,
		`env -i LABEL=value mtix inbox --format prompt | next-runtime`,
		`command -p mtix inbox --format prompt | next-runtime`,
		`"${MTIX_BIN}" inbox --format prompt | next-runtime`,
	} {
		t.Run(command, func(t *testing.T) {
			require.NotEmpty(t, wakeLaunchProblems("```sh\n"+command+"\n```", true))
		})
	}
}

func TestWakeGuidance_DerivedFixture_AcceptsRoutine(t *testing.T) {
	require.Empty(t, wakeDerivedProblems(`printf '%s\n' "$MESSAGE" | claude -p`))
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	require.Empty(t, wakeDerivedProblems(`printf '%s\n' "$FIXTURE_INPUT" | claude -p`, source+"\n"))
}

func TestWakeGuidance_CodeDelimiters_PreserveCommands(t *testing.T) {
	for _, text := range []string{
		"Use `courier\n\"$PAYLOAD\"`.",
		"An unmatched ``` followed by `courier \"$PAYLOAD\"`.",
		"Use `` courier `$PAYLOAD` finished ``.",
		"| command | `` courier `$PAYLOAD` finished `` | explanation |",
	} {
		t.Run(text, func(t *testing.T) { require.NotEmpty(t, wakeLaunchProblems(text, true)) })
	}
}
