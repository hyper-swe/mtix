// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendProjectPointer_IndentedExamplesPreservesBytes(t *testing.T) {
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		pointer := "@MTIX.md"
		if name == "AGENTS.md" {
			pointer = agentsPointer
		}
		for _, indent := range []string{"    ", "     ", "\t", " \t", "   \t"} {
			for _, prefix := range []string{"# Example\r\n\r\n", "Paragraph\r\n"} {
				for _, end := range []string{"\r\n", ""} {
					input := prefix + indent + pointer + end
					got, err := appendProjectPointer([]byte(input), pointer, name)
					require.NoError(t, err)
					separator := ""
					if end == "" {
						separator = "\n"
					}
					require.Equal(t, input+separator+pointer+"\n", string(got))
					again, err := appendProjectPointer(got, pointer, name)
					require.NoError(t, err)
					require.Equal(t, got, again)
				}
			}
		}
	}
}

func TestAppendProjectPointer_LiveWhitespaceCompatibility(t *testing.T) {
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		pointer := "@MTIX.md"
		if name == "AGENTS.md" {
			pointer = agentsPointer
		}
		for _, leading := range []string{"", " ", "  ", "   ", "\u00a0", "\u2003"} {
			for _, trailing := range []string{"", " \t", "\u00a0\u2003"} {
				input := "# User\r\n\r\n" + leading + pointer + trailing
				got, err := appendProjectPointer([]byte(input), pointer, name)
				require.NoError(t, err)
				require.Equal(t, input, string(got))
			}
		}
		input := "    example\r\n" + pointer + "\r\n"
		got, err := appendProjectPointer([]byte(input), pointer, name)
		require.NoError(t, err)
		require.Equal(t, input, string(got))
	}
}
