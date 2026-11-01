// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostDispatcher_CLIReferenceMatchesActualGenerator(t *testing.T) {
	actual, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	generated := generatedHelpReference(t)
	section := func(text string) string {
		start := strings.Index(text, "\n## dispatcher\n")
		require.NotEqual(t, -1, start)
		end := strings.Index(text[start:], "\n## docs\n")
		require.NotEqual(t, -1, end)
		return text[start : start+end]
	}
	require.Equal(t, section(generated), section(string(actual)))
	root := newRootCmd()
	for _, verb := range []string{"", "run", "install", "start", "stop", "status", "uninstall"} {
		t.Run(verb, func(t *testing.T) {
			path := []string{"dispatcher"}
			if verb != "" {
				path = append(path, verb)
			}
			command, _, err := root.Find(path)
			require.NoError(t, err)
			require.Equal(t, cliReferenceSection(t, section(generated), command.Name(), command.Use), cliReferenceSection(t, section(string(actual)), command.Name(), command.Use))
		})
	}
}
