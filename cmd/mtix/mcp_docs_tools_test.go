// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// These tests keep shipped MCP instructions aligned with the registry wired
// by the running binary (FR-13.1, MTIX-107.90), including plugin frontmatter.
import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
)

func loadMCPInstructionDocs(root string) (map[string]string, error) {
	documents := make(map[string]string)
	for _, dir := range []string{"internal/docs/templates", ".claude-plugin", ".codex-plugin", "docs"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || (filepath.Ext(path) != ".md" && filepath.Ext(path) != ".tmpl") {
				return nil
			}
			return readMCPInstructionDoc(root, path, documents)
		})
		if err != nil {
			return nil, fmt.Errorf("walk shipped docs %s: %w", dir, err)
		}
	}
	for _, name := range []string{"README.md", "USERMANUAL.md"} {
		if err := readMCPInstructionDoc(root, filepath.Join(root, name), documents); err != nil {
			return nil, fmt.Errorf("read shipped doc %s: %w", name, err)
		}
	}
	return documents, nil
}

func readMCPInstructionDoc(root, path string, documents map[string]string) error {
	body, err := os.ReadFile(path) //nolint:gosec // test inventory reads only repository or fixture roots
	if err != nil {
		return fmt.Errorf("read instruction %s: %w", path, err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("relative instruction path %s: %w", path, err)
	}
	documents[filepath.ToSlash(rel)] = string(body)
	return nil
}

// mcpDocDataReference describes one data token in a specific literal context.
// Paths below locate shipped examples, not exemptions for their whole contents.
type mcpDocDataReference struct {
	name    string
	context string
}

func mcpDocDataReferences(path string) []mcpDocDataReference {
	switch path {
	case "docs/EXPORT-FORMAT.md":
		return []mcpDocDataReference{{"mtix_version", "| `mtix_version` | Version of the mtix that wrote the file (may be empty). |"}}
	case "docs/SYNC-PROTOCOL.md":
		return []mcpDocDataReference{{"mtix_sync_migration", "hash of the constant string `\"mtix_sync_migration\"`"}}
	case "docs/SYNC-DESIGN.md":
		return []mcpDocDataReference{
			{"mtix_user", "ALTER ROLE mtix_user WITH PASSWORD '...'"},
			{"mtix_sync_migration", "pg_advisory_xact_lock(hash('mtix_sync_migration'))"},
		}
	case "README.md", "USERMANUAL.md":
		refs := mcpDocDSNDataReferences()
		if path == "USERMANUAL.md" {
			refs = append(refs, mcpDocDataReference{"mtix_team", "mtix sync harden --apply --keep-role mtix_team"},
				mcpDocDataReference{"mtix_team", "# restrict to the owner and mtix_team"},
				mcpDocDataReference{"mtix_team", "mtix config set sync.keep_roles mtix_team"},
				mcpDocDataReference{"mtix_team", "# keep mtix_team in later runs; also doctor strict mode"})
		}
		return refs
	case "internal/docs/templates/workflows/safety-critical.md.tmpl":
		return []mcpDocDataReference{{"mtix_sync", "Provision the hub, create the least-privilege role (`mtix_sync`),"}}
	case "internal/docs/templates/workflows/small-team.md.tmpl":
		return append(mcpDocSmallTeamDataReferences(), mcpDocDSNDataReferences()...)
	case "internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md":
		return mcpDocSQLDataReferences()
	case "docs/SECURITY-MODEL.md":
		return append(mcpDocSQLDataReferences(), mcpDocDataReference{"mtix_writer",
			"and `mtix_writer` with the syncing role)"})
	default:
		return nil
	}
}

func mcpDocDSNDataReferences() []mcpDocDataReference {
	const example = "postgresql://mtix_sync@hub.example.com:5432/mtix_hub?sslmode=verify-full"
	return []mcpDocDataReference{{"mtix_sync", example}, {"mtix_hub", example}}
}

func mcpDocSmallTeamDataReferences() []mcpDocDataReference {
	return []mcpDocDataReference{
		{"mtix_sync", "Run as a PG superuser once during provisioning. `mtix_sync` runs"},
		{"mtix_sync", "the roles named here; `mtix_sync` owns only the objects `mtix sync init`"},
		{"mtix_sync", "CREATE ROLE mtix_sync LOGIN PASSWORD 'set-a-strong-one';"},
		{"mtix_hub", "CREATE DATABASE mtix_hub;"},
		{"mtix_hub", "\\c mtix_hub"},
		{"mtix_hub", "GRANT CONNECT ON DATABASE mtix_hub TO mtix_sync;"},
		{"mtix_sync", "GRANT CONNECT ON DATABASE mtix_hub TO mtix_sync;"},
		{"mtix_sync", "GRANT USAGE, CREATE ON SCHEMA public TO mtix_sync;"},
		{"mtix_sync", "REVOKE CREATE ON SCHEMA public FROM mtix_sync;"},
		{"mtix_sync", "GRANT CREATE ON SCHEMA public TO mtix_sync;"},
	}
}

func mcpDocSQLDataReferences() []mcpDocDataReference {
	return []mcpDocDataReference{
		{"mtix_owner", "\\password mtix_owner"},
		{"mtix_writer", "\\password mtix_writer"},
		{"mtix_owner", "CREATE ROLE mtix_owner LOGIN;"},
		{"mtix_writer", "CREATE ROLE mtix_writer LOGIN;"},
		{"mtix_hub", "CREATE DATABASE mtix_hub OWNER mtix_owner;"},
		{"mtix_owner", "CREATE DATABASE mtix_hub OWNER mtix_owner;"},
		{"mtix_hub", "\\c mtix_hub"},
		{"mtix_owner", "GRANT USAGE, CREATE ON SCHEMA public TO mtix_owner;"},
		{"mtix_hub", "GRANT CONNECT ON DATABASE mtix_hub TO mtix_writer;"},
		{"mtix_writer", "GRANT CONNECT ON DATABASE mtix_hub TO mtix_writer;"},
		{"mtix_writer", "GRANT USAGE ON SCHEMA public TO mtix_writer;"},
		{"mtix_writer", "GRANT SELECT ON TABLE sync_events, sync_hub_state, sync_node_collisions, sync_project_clients TO mtix_writer;"},
		{"mtix_writer", "GRANT INSERT ON TABLE sync_events, sync_conflicts, sync_project_clients TO mtix_writer;"},
		{"mtix_writer", "GRANT UPDATE ON TABLE sync_project_clients TO mtix_writer;"},
		{"mtix_writer", "GRANT USAGE ON SEQUENCE sync_conflicts_conflict_id_seq TO mtix_writer;"},
		{"mtix_writer", "GRANT EXECUTE ON FUNCTION record_restore_collision TO mtix_writer;"},
		{"mtix_writer", "GRANT UPDATE ON TABLE sync_node_collisions TO mtix_writer;"},
		{"mtix_writer", "GRANT SELECT ON TABLE node_renumber_remaps TO mtix_writer;"},
		{"mtix_writer", "GRANT INSERT ON TABLE node_renumber_remaps TO mtix_writer;"},
	}
}

// isMCPDocDataReference matches only this occurrence within an explicit data
// example. Another occurrence of the same name on the same line is checked
// as a tool reference. Prefixed names never use these exemptions.
func isMCPDocDataReference(path, line, name string, at int) bool {
	for _, ref := range mcpDocDataReferences(path) {
		if ref.name != name {
			continue
		}
		for from := 0; from < len(line); {
			pos := strings.Index(line[from:], ref.context)
			if pos < 0 {
				break
			}
			pos += from
			if at == pos+strings.Index(ref.context, name) {
				return true
			}
			from = pos + len(ref.context)
		}
	}
	return false
}

func unknownMCPInstructionTools(documents map[string]string, reg *mcp.ToolRegistry) []string {
	registered := make(map[string]bool)
	for _, tool := range reg.List() {
		registered[tool.Name] = true
	}
	toolName := regexp.MustCompile(`mcp__mtix__[A-Za-z0-9_-]+|\bmtix_[A-Za-z0-9_-]+`)
	var problems []string
	for path, text := range documents {
		for i, line := range strings.Split(text, "\n") {
			for _, span := range toolName.FindAllStringIndex(line, -1) {
				name := line[span[0]:span[1]]
				prefixed := strings.HasPrefix(name, "mcp__mtix__")
				if registered[strings.TrimPrefix(name, "mcp__mtix__")] {
					continue
				}
				if !prefixed && isMCPDocDataReference(path, line, name, span[0]) {
					continue
				}
				problems = append(problems, fmt.Sprintf("%s:%d: %s", path, i+1, name))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func TestMCPInstructionDocs_EveryToolExistsInBuild(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	documents, err := loadMCPInstructionDocs(root)
	require.NoError(t, err)
	for _, path := range []string{
		"internal/docs/templates/skill.md.tmpl", ".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md", "docs/SKILL.md", "docs/CLI_REFERENCE.md",
		"README.md", "USERMANUAL.md", "docs/AGENTS.md", "docs/SYNC-DESIGN.md",
	} {
		require.Contains(t, documents, path, "the inventory must reach every shipped surface")
	}
	require.Contains(t, documents["internal/docs/templates/skill.md.tmpl"], "mcp__mtix__mtix_context")
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	require.Contains(t, documents["docs/AGENTS.md"], "`mtix_context`")
	require.Contains(t, documents["docs/SYNC-DESIGN.md"], "CONFLICT block")
	require.Greater(t, len(reg.List()), 30, "the scan uses the fully wired runtime registry")
	require.Empty(t, unknownMCPInstructionTools(documents, reg), "shipped docs name tools absent from this build")
}

func TestMCPInstructionDocs_CheckerRejectsPlantedUnknownTool(t *testing.T) {
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"known tools", "`mcp__mtix__mtix_context` and mcp__mtix__mtix_inbox", nil},
		{"bare known tools", "`mtix_context` and `mtix_inbox`", nil},
		{"bare prose unknown", "Call `mtix_planted_unknown`.", []string{"fixture.md:1: mtix_planted_unknown"}},
		{"bare known prefix unknown suffix", "`mtix_show_unknown`", []string{"fixture.md:1: mtix_show_unknown"}},
		{"bare fenced unknown", "```\nCall `mtix_planted_unknown`\n```", []string{"fixture.md:2: mtix_planted_unknown"}},
		{"mixed representations", "`mtix_planted_unknown` mcp__mtix__mtix_show_unknown", []string{"fixture.md:1: mcp__mtix__mtix_show_unknown", "fixture.md:1: mtix_planted_unknown"}},
		{"CLI command", "`mtix verify` and `mtix dep show <id>`", nil},
		{"empty text", "", nil},
		{"prefix description", "Use the mcp__mtix__ prefix", nil},
		{"prose", "Call mcp__mtix__mtix_planted_unknown.", []string{"fixture.md:1: mcp__mtix__mtix_planted_unknown"}},
		{"frontmatter", "allowed-tools:\n  - mcp__mtix__mtix_planted_unknown\n", []string{"fixture.md:2: mcp__mtix__mtix_planted_unknown"}},
		{"code block", "```\nmcp__mtix__mtix_planted_unknown\n```", []string{"fixture.md:2: mcp__mtix__mtix_planted_unknown"}},
		{"known prefix unknown suffix", "mcp__mtix__mtix_show_unknown", []string{"fixture.md:1: mcp__mtix__mtix_show_unknown"}},
		{"multiple tools", "mcp__mtix__mtix_show mcp__mtix__mtix_planted_unknown", []string{"fixture.md:1: mcp__mtix__mtix_planted_unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, unknownMCPInstructionTools(map[string]string{"fixture.md": tt.text}, reg))
		})
	}
}

func TestMCPInstructionDocs_InventoryIncludesNestedDocsAndReferences(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		"internal/docs/templates/nested/instruction.md.tmpl", ".claude-plugin/skills/nested/SKILL.md",
		".codex-plugin/skills/nested/SKILL.md", "docs/nested/instruction.md", "docs/CLI_REFERENCE.md",
		"README.md", "USERMANUAL.md", "docs/AGENTS.md", "docs/SYNC-DESIGN.md",
	}
	for _, path := range paths {
		file := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o750))
		require.NoError(t, os.WriteFile(file, []byte("mcp__mtix__mtix_planted_unknown\nCall `mtix_planted_unknown`.\nCall mtix_planted_unknown."), 0o600))
	}
	documents, err := loadMCPInstructionDocs(root)
	require.NoError(t, err)
	require.Len(t, documents, len(paths))
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	for _, path := range paths {
		require.Equal(t, "mcp__mtix__mtix_planted_unknown\nCall `mtix_planted_unknown`.\nCall mtix_planted_unknown.", documents[path])
		require.Contains(t, unknownMCPInstructionTools(documents, reg), path+":1: mcp__mtix__mtix_planted_unknown")
		require.Contains(t, unknownMCPInstructionTools(documents, reg), path+":2: mtix_planted_unknown")
		require.Contains(t, unknownMCPInstructionTools(documents, reg), path+":3: mtix_planted_unknown")
	}
}

func TestMCPInstructionDocs_DataReferencesExemptOnlyExactDeclarations(t *testing.T) {
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	tests := []struct {
		path        string
		name        string
		declaration string
	}{
		{"docs/EXPORT-FORMAT.md", "mtix_version", "| `mtix_version` | Version of the mtix that wrote the file (may be empty). |"},
		{"docs/SECURITY-MODEL.md", "mtix_writer", "As statements, run by the table owner (replace `public` if the hub lives in another schema, and `mtix_writer` with the syncing role). This is the same SQL the sync skill gives, and a test compares the two:"},
		{"internal/docs/templates/workflows/safety-critical.md.tmpl", "mtix_sync", "Provision the hub, create the least-privilege role (`mtix_sync`),"},
		{"internal/docs/templates/workflows/small-team.md.tmpl", "mtix_sync", "Run as a PG superuser once during provisioning. `mtix_sync` runs"},
		{"internal/docs/templates/workflows/small-team.md.tmpl", "mtix_sync", "the roles named here; `mtix_sync` owns only the objects `mtix sync init`"},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("%s-%d", tt.name, i), func(t *testing.T) {
			require.True(t, isMCPDocDataReference(tt.path, tt.declaration, tt.name, strings.Index(tt.declaration, tt.name)))
			require.False(t, isMCPDocDataReference(tt.path, tt.declaration, "mtix_planted_unknown", strings.Index(tt.declaration, tt.name)))
			require.Empty(t, unknownMCPInstructionTools(map[string]string{tt.path: tt.declaration}, reg))
			require.Equal(t, []string{tt.path + ":2: mtix_planted_unknown"},
				unknownMCPInstructionTools(map[string]string{tt.path: tt.declaration + "\nCall `mtix_planted_unknown`."}, reg))
			for _, instruction := range []string{
				"Call `" + tt.name + "`.", tt.declaration + " Call `" + tt.name + "`.",
				"Call mcp__mtix__" + tt.name + ".",
			} {
				require.NotEmpty(t, unknownMCPInstructionTools(map[string]string{tt.path: instruction}, reg), instruction)
			}
			require.NotEmpty(t, unknownMCPInstructionTools(map[string]string{"other.md": tt.declaration}, reg))
		})
	}
}

func TestMCPInstructionDocs_UnquotedProseAndFencesRejectUnknownTools(t *testing.T) {
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	tests := []struct {
		name string
		path string
		text string
		want []string
	}{
		{"registered prose", "fixture.md", "Call mtix_annotate.", nil},
		{"unquoted prose", "fixture.md", "Call mtix_planted_unknown.", []string{"fixture.md:1: mtix_planted_unknown"}},
		{"instruction fence", "fixture.md", "```\nPer AGENTS.md, escalate via mtix_planted_unknown.\n```", []string{"fixture.md:2: mtix_planted_unknown"}},
		{"SQL fence instruction", "fixture.md", "```sql\nCall mtix_planted_unknown.\n```", []string{"fixture.md:2: mtix_planted_unknown"}},
		{"schema plus unquoted unknown", "docs/EXPORT-FORMAT.md", "| `mtix_version` | Version of the mtix that wrote the file (may be empty). | Call mtix_planted_unknown.", []string{"docs/EXPORT-FORMAT.md:1: mtix_planted_unknown"}},
		{"SQL role plus unquoted unknown", "docs/SECURITY-MODEL.md", "GRANT USAGE ON SCHEMA public TO mtix_writer; Call mtix_planted_unknown.", []string{"docs/SECURITY-MODEL.md:1: mtix_planted_unknown"}},
		{"SQL role plus misused role name", "docs/SECURITY-MODEL.md", "GRANT USAGE ON SCHEMA public TO mtix_writer; Call mtix_writer.", []string{"docs/SECURITY-MODEL.md:1: mtix_writer"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, unknownMCPInstructionTools(map[string]string{tt.path: tt.text}, reg))
		})
	}
}

func TestMCPInstructionDocs_SQLLockAndConfigDataCannotHideToolCalls(t *testing.T) {
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	tests := []struct {
		path string
		name string
		data string
	}{
		{"README.md", "mtix_hub", "postgresql://mtix_sync@hub.example.com:5432/mtix_hub?sslmode=verify-full"},
		{"USERMANUAL.md", "mtix_team", "mtix sync harden --apply --keep-role mtix_team # restrict to the owner and mtix_team"},
		{"USERMANUAL.md", "mtix_team", "mtix config set sync.keep_roles mtix_team # keep mtix_team in later runs; also doctor strict mode"},
		{"docs/SYNC-DESIGN.md", "mtix_user", "`ALTER ROLE mtix_user WITH PASSWORD '...'`"},
		{"docs/SYNC-DESIGN.md", "mtix_sync_migration", "`pg_advisory_xact_lock(hash('mtix_sync_migration'))`"},
		{"docs/SYNC-PROTOCOL.md", "mtix_sync_migration", "hash of the constant string `\"mtix_sync_migration\"`"},
		{"internal/docs/templates/skills/sync.md.tmpl", "mtix_owner", "CREATE DATABASE mtix_hub OWNER mtix_owner;"},
		{"internal/docs/templates/workflows/small-team.md.tmpl", "mtix_sync", "GRANT USAGE, CREATE ON SCHEMA public TO mtix_sync;"},
		{".claude-plugin/skills/mtix-sync.md", "mtix_writer", "GRANT USAGE ON SCHEMA public TO mtix_writer;"},
		{".codex-plugin/skills/sync/SKILL.md", "mtix_writer", "`\\password mtix_writer`"},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("%s-%d", tt.name, i), func(t *testing.T) {
			require.Empty(t, unknownMCPInstructionTools(map[string]string{tt.path: tt.data}, reg))
			require.Equal(t, []string{tt.path + ":1: mtix_planted_unknown"}, unknownMCPInstructionTools(
				map[string]string{tt.path: tt.data + " Call mtix_planted_unknown."}, reg))
			require.Equal(t, []string{tt.path + ":1: " + tt.name}, unknownMCPInstructionTools(
				map[string]string{tt.path: tt.data + " Call " + tt.name + "."}, reg))
			require.Equal(t, []string{tt.path + ":1: mcp__mtix__" + tt.name}, unknownMCPInstructionTools(
				map[string]string{tt.path: "Call mcp__mtix__" + tt.name + "."}, reg))
			require.NotEmpty(t, unknownMCPInstructionTools(map[string]string{"other.md": tt.data}, reg))
		})
	}
}

func TestMCPInstructionDocs_ArbitraryPrefixedNamesNeverEscapeValidation(t *testing.T) {
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	tests := []struct {
		name string
		path string
		text string
		want []string
	}{
		{"unquoted suffix", "fixture.md", "Call mcp__mtix__planted_unknown.", []string{"fixture.md:1: mcp__mtix__planted_unknown"}},
		{"backticked suffix", "fixture.md", "Call `mcp__mtix__planted_unknown`.", []string{"fixture.md:1: mcp__mtix__planted_unknown"}},
		{"fenced suffix", "fixture.md", "```\nCall mcp__mtix__planted_unknown.\n```", []string{"fixture.md:2: mcp__mtix__planted_unknown"}},
		{"uppercase suffix", "fixture.md", "mcp__mtix__UNKNOWN", []string{"fixture.md:1: mcp__mtix__UNKNOWN"}},
		{"digit suffix", "fixture.md", "mcp__mtix__42", []string{"fixture.md:1: mcp__mtix__42"}},
		{"hyphen suffix", "fixture.md", "mcp__mtix__unknown-tool", []string{"fixture.md:1: mcp__mtix__unknown-tool"}},
		{"adjacent data", "docs/SECURITY-MODEL.md", "GRANT USAGE ON SCHEMA public TO mtix_writer; Call mcp__mtix__planted_unknown.", []string{"docs/SECURITY-MODEL.md:1: mcp__mtix__planted_unknown"}},
		{"role suffix", "docs/SECURITY-MODEL.md", "GRANT USAGE ON SCHEMA public TO mtix_writer; Call mcp__mtix__mtix_writer.", []string{"docs/SECURITY-MODEL.md:1: mcp__mtix__mtix_writer"}},
		{"schema suffix", "docs/EXPORT-FORMAT.md", "| `mtix_version` | Version of the mtix that wrote the file (may be empty). | Call mcp__mtix__mtix_version.", []string{"docs/EXPORT-FORMAT.md:1: mcp__mtix__mtix_version"}},
		{"registered prefix", "fixture.md", "Call mcp__mtix__mtix_annotate.", nil},
		{"empty suffix description", "fixture.md", "Use the mcp__mtix__ prefix.", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, unknownMCPInstructionTools(map[string]string{tt.path: tt.text}, reg))
		})
	}
}
