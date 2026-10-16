// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Documentation diagnostics exercise the real CLI without changing document state.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type doctorDocumentSnapshot struct {
	content string
	mode    os.FileMode
	mtime   time.Time
}

func doctorDocsSnapshot(t *testing.T, root string) map[string]doctorDocumentSnapshot {
	t.Helper()
	got := make(map[string]doctorDocumentSnapshot)
	for _, name := range []string{
		"MTIX.md", "CLAUDE.md", "AGENTS.md", "HYPERSWE.md", ".gitignore", ".git/info/exclude",
		".mtix/docs/AGENTS.md", ".mtix/docs/CLAUDE.md", ".mtix/docs/SKILL.md", ".mtix/docs/CLI_REFERENCE.md",
		".mtix/docs/STATUS_MACHINE.md", ".mtix/docs/WORKFLOWS.md", ".mtix/docs/CONTEXT_CHAIN.md",
		".mtix/docs/BLOCKED_HANDLING.md", ".mtix/docs/SESSION_PROTOCOL.md", ".mtix/docs/PATTERNS.md",
		".mtix/docs/TROUBLESHOOTING.md", ".mtix/docs/workflows/solo.md", ".mtix/docs/workflows/small-team.md",
		".mtix/docs/workflows/safety-critical.md",
	} {
		info, err := os.Lstat(filepath.Join(root, name))
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		body, readErr := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, readErr)
		got[name] = doctorDocumentSnapshot{string(body), info.Mode(), info.ModTime()}
	}
	return got
}

func doctorDocsRow(t *testing.T, body string) map[string]string {
	t.Helper()
	for _, row := range doctorCLIRows(t, body) {
		if row["id"] == "docs.stale" {
			return row
		}
	}
	t.Fatal("configured doctor must register docs.stale")
	return nil
}

func doctorDocsWrite(t *testing.T, root, name, text string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(text), 0600))
}

func TestDoctorDocs_FreshStaleAndPointers_ReadOnlyJSON(t *testing.T) {
	cases := []struct {
		name, file, content, status string
		remove, hyperswe            bool
	}{
		{name: "fresh", status: "pass"},
		{name: "stale", file: "MTIX.md", content: "private document bytes", status: "warn"},
		{name: "missing-briefing", file: "MTIX.md", remove: true, status: "warn"},
		{name: "missing-claude", file: "CLAUDE.md", remove: true, status: "warn"},
		{name: "missing-agents", file: "AGENTS.md", remove: true, status: "warn"},
		{name: "empty-claude", file: "CLAUDE.md", status: "warn"},
		{name: "duplicate-claude", file: "CLAUDE.md", content: "@MTIX.md\nuser\n@MTIX.md\n", status: "warn"},
		{name: "duplicate-agents", file: "AGENTS.md", content: "See MTIX.md for mtix instructions.\nRead [MTIX.md](MTIX.md) for mtix instructions.\n", status: "warn"},
		{name: "fenced-example", file: "CLAUDE.md", content: "~~~\n@MTIX.md\n~~~\n", status: "warn"},
		{name: "indented-examples", file: "CLAUDE.md", content: "    @MTIX.md\n\t@MTIX.md\n", status: "warn"},
		{name: "hyperswe-no-pointers", hyperswe: true, status: "pass"},
		{name: "hyperswe-existing", hyperswe: true, file: "CLAUDE.md", content: "@MTIX.md\n", status: "pass"},
		{name: "hyperswe-duplicate", hyperswe: true, file: "CLAUDE.md", content: "@MTIX.md\n@MTIX.md\n", status: "warn"},
		{name: "hyperswe-stale", hyperswe: true, file: "MTIX.md", content: "private document bytes", status: "warn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			if tc.hyperswe {
				doctorDocsWrite(t, root, "HYPERSWE.md", "owner bytes\r\n")
			}
			generated := doctorCLIExecute(t, root, "", "docs", "generate")
			require.Zero(t, generated.exit, generated.stderr)
			if tc.remove {
				require.NoError(t, os.Remove(filepath.Join(root, tc.file)))
			} else if tc.file != "" {
				doctorDocsWrite(t, root, tc.file, tc.content)
			}
			before := doctorDocsSnapshot(t, root)
			for _, flags := range [][]string{{"doctor", "--json"}, {"doctor", "--json", "--fix"}} {
				out := doctorCLIExecute(t, root, "", flags...)
				require.Zero(t, out.exit, out.stderr)
				row := doctorDocsRow(t, out.stdout)
				assert.Equal(t, tc.status, row["status"])
				assert.NotContains(t, row["detail"], "private document bytes")
				if tc.status == "warn" {
					assert.Contains(t, row["fix"], "mtix -C ")
					assert.Contains(t, row["fix"], root)
					assert.True(t, strings.HasSuffix(row["fix"], " docs generate"))
				}
				assert.Equal(t, before, doctorDocsSnapshot(t, root))
			}
		})
	}
}
