// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/stretchr/testify/require"
)

// mtixToolName matches an mtix_* identifier. A client-prefixed name such as
// mtix__mtix_show yields its inner mtix_show, because the leading "mtix__"
// does not match the pattern (two underscores).
var mtixToolName = regexp.MustCompile(`mtix_[a-z][a-z0-9_]*`)

// nonToolIdentifiers are mtix_* names the docs use that are not MCP tools:
// example PostgreSQL role and database names in the sync skill.
var nonToolIdentifiers = map[string]bool{
	"mtix_owner":  true,
	"mtix_writer": true,
	"mtix_hub":    true,
	"mtix_sync":   true,
}

// registeredToolNames returns the names of every tool the MCP server
// registers, using the same registrars as the mtix mcp command.
func registeredToolNames() map[string]bool {
	reg := mcp.NewToolRegistry()
	mcp.RegisterNodeTools(reg, nil)
	mcp.RegisterWorkflowTools(reg, nil, nil)
	mcp.RegisterContextTools(reg, nil, nil)
	mcp.RegisterDepTools(reg, nil)
	mcp.RegisterInboxTools(reg, nil)
	mcp.RegisterSessionTools(reg, nil, nil)
	mcp.RegisterAnalyticsTools(reg, nil, nil, nil)
	mcp.RegisterDocsTools(reg)
	mcp.RegisterSyncWorkflowTool(reg, nil, "")
	names := map[string]bool{}
	for _, def := range reg.List() {
		names[def.Name] = true
	}
	return names
}

// unregisteredToolNames returns the sorted mtix_* names in body that are
// neither registered tools nor known non-tool identifiers.
func unregisteredToolNames(body string, registered map[string]bool) []string {
	seen := map[string]bool{}
	for _, name := range mtixToolName.FindAllString(body, -1) {
		if !registered[name] && !nonToolIdentifiers[name] {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestShippedDocs_ToolNames_AreRegisteredMCPTools: every mtix_* name in the
// templates and the plugin mirrors is a tool the MCP server registers, so an
// agent following a skill never calls a tool that does not exist (MTIX-107.42).
func TestShippedDocs_ToolNames_AreRegisteredMCPTools(t *testing.T) {
	registered := registeredToolNames()
	require.NotEmpty(t, registered)
	require.True(t, registered["mtix_show"], "registrars cover the core tools")

	roots := []string{
		filepath.Join("templates"),
		filepath.Join("..", "..", ".claude-plugin"),
		filepath.Join("..", "..", ".codex-plugin"),
	}
	scanned := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, readErr := os.ReadFile(path) //nolint:gosec // path from WalkDir over repository docs
			if readErr != nil {
				return readErr
			}
			scanned++
			require.Empty(t, unregisteredToolNames(string(body), registered),
				"%s names mtix_* tools the MCP server does not register", path)
			return nil
		})
		require.NoError(t, err)
	}
	require.Positive(t, scanned)
}

// TestUnregisteredToolNames_FakeNameInTemplateCopy_IsReported: the check
// fails for an unregistered name inserted into a copy of the admin template,
// and passes for the unmodified copy.
func TestUnregisteredToolNames_FakeNameInTemplateCopy_IsReported(t *testing.T) {
	registered := registeredToolNames()
	body, err := os.ReadFile(filepath.Join("templates", "skills", "admin.md.tmpl"))
	require.NoError(t, err)

	require.Empty(t, unregisteredToolNames(string(body), registered))

	mutated := string(body) + "\nCall `mtix_backup` or `mtix_gc` to maintain the store.\n"
	require.Equal(t, []string{"mtix_backup", "mtix_gc"}, unregisteredToolNames(mutated, registered))
}
