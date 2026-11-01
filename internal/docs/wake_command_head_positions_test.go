// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestWakeGuidance_CommandHeadPositions(t *testing.T) {
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, head := range []string{"@", "ñ", "-", "+", "?", "'@'", `\@`, "'ñ'", `"@"`, "!", ":", ".", "/", "=", ",", "~", "☃", "''", `""`} {
		t.Run(head, func(t *testing.T) {
			for _, verb := range []string{"inbox --agent w", "date", "--help"} {
				command := head + " " + verb
				for _, frame := range []struct {
					name, text string
					doc        bool
				}{
					{"raw", command, false}, {"span", "Use `" + command + "`.", true},
					{"fence", "```sh\n" + command + "\n```", true}, {"script", script + "\n" + command, false},
				} {
					t.Run(verb+"/"+frame.name, func(t *testing.T) {
						require.Equal(t, verb != "inbox --agent w", len(wakeLaunchProblems(frame.text, frame.doc)) == 0, "%s", command)
					})
				}
			}
		})
	}
	assertWakeTokenPolicy(t, "$M --help", true)
	require.False(t, wakeUnknownReaderSource("inbox"))
	require.True(t, wakeUnknownReaderSource(strings.Repeat("@", 3)+" inbox"))
}
