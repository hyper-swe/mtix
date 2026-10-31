// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-112.26 refuses a reader definition even when its closing brace is absent.
func TestWakeGuidance_UnterminatedDefinition_Checked(t *testing.T) {
	for _, body := range []string{"fetch() {\nmtix inbox --format prompt", "# fetch() {\n# mtix inbox --format prompt"} {
		require.NotEmpty(t, wakeLaunchProblems("```sh\n"+body+"\n```", true))
		control := strings.ReplaceAll(body, "mtix inbox --format prompt", "date -u")
		require.Empty(t, wakeLaunchProblems("```sh\n"+control+"\n```", true))
	}
}

// A commented definition ends at its own brace without absorbing later text.
func TestWakeGuidance_CommentedDefinition_PreservesBoundary(t *testing.T) {
	text := "# fetch() {\n# mtix inbox --format prompt\n# }\ndate -u\n}"
	want := "fetch() {\nmtix inbox --format prompt\n}\n"
	require.Equal(t, []string{want}, wakeReaderDefinitions(strings.Split(text, "\n")))
	require.NotEmpty(t, wakeLaunchProblems(text))
	control := strings.ReplaceAll(text, "mtix inbox --format prompt", "date -u")
	require.Empty(t, wakeReaderDefinitions(strings.Split(control, "\n")))
	require.Empty(t, wakeLaunchProblems(control))
}

// Unknown command heads with an inbox token are sources without expansion.
func TestWakeGuidance_UnknownReaderHeads_Checked(t *testing.T) {
	for _, head := range []string{`$M`, `${M}`, `"$M"`, "fetch", "next-tool"} {
		for _, flags := range []string{"", "-C board ", "--project-dir=board "} {
			reader := head + " " + flags + "inbox --format prompt"
			for _, text := range []string{reader, "M=mtix; " + reader, "alias fetch=mtix; " + reader,
				"MODE=on " + reader, "command -- " + reader, "fetch() { " + reader + "; }",
				`FRESH="$(` + reader + `)"` + "\n" + `next-runtime "$FRESH"`,
				`next-runtime "$(` + reader + `)"`, `read FRESH < <(` + reader + `)`,
			} {
				require.NotEmpty(t, wakeLaunchProblems(text), "%s", text)
				require.NotEmpty(t, wakeLaunchProblems("```sh\n"+text+"\n```", true), "%s", text)
				require.NotEmpty(t, wakeLaunchProblems("Use `"+text+"`.", true), "%s", text)
			}
		}
	}
	for _, text := range []string{`$M --help`, `${M} --help`, `"$M" --help`, `$TOOL run`, "fetch --help",
		`printf '%s\n' input`, `git add input`, `NOW=$(date -u)`,
	} {
		require.Empty(t, wakeLaunchProblems(text), "%s", text)
		require.Empty(t, wakeLaunchProblems("Use `"+text+"`.", true), "%s", text)
	}
	require.Empty(t, wakeLaunchProblems("Run $M inbox in an explanatory paragraph.", true))
}

// Unknown readers cannot inherit the known mtix metadata-command allowance.
func TestWakeGuidance_UnknownReaderMetadata_Refused(t *testing.T) {
	for _, text := range []string{`$M inbox lease acquire`, `$M inbox ack`, `fetch inbox lease release`, `next-tool inbox ack`} {
		require.NotEmpty(t, wakeLaunchProblems(text), "%s", text)
		require.NotEmpty(t, wakeLaunchProblems("Use `"+text+"`.", true), "%s", text)
	}
	for _, text := range []string{`mtix inbox lease acquire`, `mtix inbox ack`, `"$MTIX_BIN" inbox lease release`} {
		require.Empty(t, wakeLaunchProblems(text), "%s", text)
	}
}
