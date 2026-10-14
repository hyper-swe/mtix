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

func TestRunDocsGenerate_ProjectIndentedExamplesAllLocalRoutes(t *testing.T) {
	routes := []struct {
		name string
		run  func() error
	}{
		{"docs", func() error { return runDocsGenerate(false) }},
		{"docs-force", func() error { return runDocsGenerate(true) }},
		{"local-pi", func() error { return runPluginInstall("pi", false) }},
		{"local-codex", func() error { return runPluginInstall("codex", false) }},
		{"local-claude", func() error { return runPluginInstall("claude-code", false) }},
	}
	for _, route := range routes {
		for _, indent := range []string{"    ", "\t"} {
			t.Run(route.name+indent, func(t *testing.T) {
				dir := projectBriefingDir(t)
				pointers := map[string]string{"CLAUDE.md": "@MTIX.md", "AGENTS.md": "Read [MTIX.md](MTIX.md) for mtix instructions."}
				originals := map[string]string{}
				for name, pointer := range pointers {
					originals[name] = "# Example\r\n\r\n" + indent + pointer + "\r\n"
					require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(originals[name]), 0600))
				}
				require.NoError(t, route.run())
				for name, pointer := range pointers {
					got := readProjectDoc(t, dir, name)
					require.Equal(t, originals[name]+pointer+"\n", got)
					require.Equal(t, 1, strings.Count(got, "\n"+pointer+"\n"))
				}
				require.NoError(t, route.run())
				for name, pointer := range pointers {
					require.Equal(t, originals[name]+pointer+"\n", readProjectDoc(t, dir, name))
				}
				require.FileExists(t, filepath.Join(dir, "MTIX.md"))
			})
		}
	}
}
