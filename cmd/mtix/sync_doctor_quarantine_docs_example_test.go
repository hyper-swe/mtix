// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Examples must show the rendered guidance, not just the command at its end.
func TestDoctorQuarantineFix_AdminDocsShowRenderedLine(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	initAckCLI(t) // Owns its store and operator configuration and clears the DSN.
	app.jsonOutput = false
	report := appendQuarantineCheck(t.Context(), DoctorReport{OverallPass: true}, app.store)
	require.Len(t, report.Checks, 1)
	var out bytes.Buffer
	printDoctorTable(&out, report)
	var fullLine string
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "fix: ") {
			fullLine = line
			break
		}
	}
	require.NotEmpty(t, fullLine, "doctor must render a recovery hint")
	for _, path := range []string{
		"internal/docs/templates/skills/admin.md.tmpl",
		".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md",
	} {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(repoRoot, path))
			require.NoError(t, err)
			require.True(t, strings.Contains(string(body), fullLine),
				"%s must include the full rendered Fix line:\n%s", path, fullLine)
		})
	}
}
