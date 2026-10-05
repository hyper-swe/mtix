// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-124: dependency guidance names the model's real types (FR-4.2/13.1).
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// dependencyTypeMentions recognizes ordinary documentation forms: named
// dependency prose, CLI/MCP options, dep_type fields, type lists and tables.
// --type on create/list commands classifies issues, so it is not a dep type.
func dependencyTypeMentions(body string) []string {
	named := "[`\"']?([A-Za-z][A-Za-z0-9_-]*)[`\"']?"
	quoted := "[`\"']([A-Za-z][A-Za-z0-9_-]*)[`\"']"
	patterns := []string{
		quoted + `\s+(?:dependency|dep)\s+types?\b`,
		`(?i:\b(?:use|uses|with|via|the|an?))\s+` + named + `\s+(?:dependency|dep)\s+types?\b`,
		quoted + `\s+(?:dependency|dependencies|dep)\b`,
		`(?i:\b(?:dependency|dep)\s+(?:of\s+)?types?)\s+` + quoted,
		`(?i:\b(?:dependency|dep)\s+(?:of\s+)?types?)\s+` + named + `(?:[.,;!?)]|\s*$|\s+(?:for|to|with|as|when|if|means|indicates|represents)\b)`,
		`(?i:\b(?:dependency|dep)\s+(?:of\s+)?types?)\s*(?::|=|\bis\b|\bare\b|\bnamed\b|\bcalled\b|\bof\b|\binclude[s]?\b)\s*` + named,
		`(?:mtix\s+dep\s+(?:add|remove)|(?:mcp__mtix__)?mtix_dep_(?:add|remove))\b[^\n` + "`" + `]*?--type(?:=|\s+)` + named,
		"[`\"']?dep_type[`\"']?\\s*[:=]\\s*" + named,
	}
	var names []string
	normalized := strings.ReplaceAll(body, "\\\n", " ")
	for _, pattern := range patterns {
		for _, match := range regexp.MustCompile(pattern).FindAllStringSubmatch(normalized, -1) {
			names = append(names, match[1])
		}
	}
	names = append(names, dependencyListMentions(body)...)
	return names
}

func dependencyListMentions(body string) []string {
	heading := regexp.MustCompile(`(?i)\b(?:dependency|dep)\s+types?\b`)
	intro := regexp.MustCompile(`(?i)^\s*(?:supported\s+)?(?:dependency|dep)\s+types?\s*(?::|=|\bare\b|\binclude[s]?\b)\s*(.*)$`)
	literal := regexp.MustCompile("[`\"']([A-Za-z][A-Za-z0-9_-]*)[`\"']")
	bullet := regexp.MustCompile(`^\s*(?:[-*+] |[0-9]+\. )(.+)`)
	var names []string
	inTypes, column := false, -1
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			inTypes = heading.MatchString(line)
			column = -1
		}
		if heading.MatchString(line) {
			for _, match := range literal.FindAllStringSubmatch(line, -1) {
				names = append(names, match[1])
			}
		}
		if match := intro.FindStringSubmatch(line); match != nil {
			inTypes = true
			names = append(names, dependencyNameList(match[1])...)
		}
		if strings.Contains(line, "|") {
			var name string
			name, column = dependencyTableName(line, inTypes, column)
			if name != "" {
				names = append(names, name)
			}
		} else {
			column = -1
			if inTypes {
				if match := bullet.FindStringSubmatch(line); match != nil {
					names = append(names, dependencyNameList(match[1])...)
				} else if strings.TrimSpace(line) != "" && !heading.MatchString(line) {
					inTypes = false
				}
			}
		}
	}
	return names
}

func dependencyNameList(text string) []string {
	separator := regexp.MustCompile(`\s*(?:,|\band\b|\bor\b|/)\s*`)
	firstName := regexp.MustCompile("^[`*\"']*([A-Za-z][A-Za-z0-9_-]*)")
	var names []string
	for _, part := range separator.Split(text, -1) {
		if match := firstName.FindStringSubmatch(strings.TrimSpace(part)); match != nil {
			names = append(names, match[1])
		}
	}
	return names
}

func dependencyTableName(line string, inTypes bool, column int) (string, int) {
	cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for i, cell := range cells {
		label := strings.ToLower(strings.Trim(strings.TrimSpace(cell), "`*"))
		if label == "dependency type" || label == "dependency types" || label == "dep type" || label == "dep types" || label == "dep_type" || (inTypes && label == "type") {
			return "", i
		}
	}
	if column < 0 || column >= len(cells) {
		return "", column
	}
	if names := dependencyNameList(cells[column]); len(names) != 0 {
		return names[0], column
	}
	return "", column
}

func undefinedDependencyTypes(documents map[string]string) []string {
	allowed := make(map[string]bool)
	for _, kind := range model.AllDepTypes() {
		allowed[string(kind)] = true
	}
	var hits []string
	for path, body := range documents {
		for _, kind := range dependencyTypeMentions(body) {
			if !allowed[kind] {
				hits = append(hits, path+": undefined dependency type "+kind)
			}
		}
	}
	sort.Strings(hits)
	return hits
}

func TestDependencyTypeDocs_RecognizesUndefinedNamesAcrossSurfaces(t *testing.T) {
	cases := []struct {
		name, body string
		invalid    bool
	}{
		{"current prose", "Use `needs_input` dependency type for requests", true},
		{"plain prose", "Use future_kind dependency type", true},
		{"type first", "Use dependency type `future_kind`", true},
		{"plain type first", "Use dependency type future_kind for requests", true},
		{"of type", "Use a dependency of type `future_kind`", true},
		{"lowercase valid type first", "use dependency type `blocks`", false},
		{"named type", "The dependency type is future_kind", true},
		{"multiple names", "Dependency types: `blocks`, `future_kind`, `related`", true},
		{"plain multiple names", "Dependency types: blocks, future_kind, related", true},
		{"multiple names before phrase", "Use `future_kind` and `blocks` dependency types", true},
		{"CLI", "mtix dep add A B --type future_kind", true},
		{"CLI equals", "mtix dep remove A B --type='future_kind'", true},
		{"MCP", "mcp__mtix__mtix_dep_add --type future_kind", true},
		{"wrapped command", "mtix dep add A B \\\n --type future_kind", true},
		{"JSON", `{"dep_type": "future_kind"}`, true},
		{"table", "| Dependency type | Purpose |\n| --- | --- |\n| `future_kind` | Requests |", true},
		{"plural table", "| Dependency types | Purpose |\n| --- | --- |\n| future_kind | Requests |", true},
		{"section table", "## Dependency Types\n\n| Type | Meaning |\n| --- | --- |\n| future_kind | Requests |", true},
		{"list", "Supported dependency types:\n- `blocks`: hard blocker\n- future_kind: request", true},
		{"issue create", "mtix create --type feature", false},
		{"hierarchy list", "mtix list --type issue", false},
		{"status", "The `blocked` status has unresolved blocking dependencies", false},
		{"arbitrary identifier", "`future_kind` is an example task label", false},
		{"numbered practice", "4. Use `blocks` dependency type for hard blockers", false},
		{"quoted table header", "| `dep_type` | Meaning |\n| --- | --- |\n| `blocks` | Hard blocker |", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := undefinedDependencyTypes(map[string]string{"x.tmpl": tc.body})
			require.Equal(t, tc.invalid, len(hits) > 0, "%v", hits)
		})
	}
}

func TestDependencyTypeDocs_EveryModelValueIsAccepted(t *testing.T) {
	for _, kind := range model.AllDepTypes() {
		t.Run(string(kind), func(t *testing.T) {
			name := string(kind)
			forms := []string{
				"Use `" + name + "` dependency type.",
				"Use " + name + " dependency type.",
				"Use dependency type `" + name + "`.",
				"Use dependency type " + name + " for requests",
				"The dependency type is " + name,
				"mtix dep add A B --type " + name,
				"mcp__mtix__mtix_dep_add --type='" + name + "'",
				"{\"dep_type\":\"" + name + "\"}",
				"Dependency types: " + name + ", " + name,
				"Dependency types:\n- `" + name + "`: supported\n",
				"| Dependency type | Purpose |\n| --- | --- |\n| `" + name + "` | Supported |",
			}
			for _, body := range forms {
				require.Contains(t, dependencyTypeMentions(body), name, body)
				require.Empty(t, undefinedDependencyTypes(map[string]string{"x.tmpl": body}), body)
			}
		})
	}
}

func TestDependencyTypeDocs_ShippedTemplatesNameOnlyModelTypes(t *testing.T) {
	set := loadShippedDocs(t)
	documents := map[string]string{}
	for path, body := range set.agent {
		if strings.HasPrefix(path, "internal/docs/templates/") {
			documents[path] = body
		}
	}
	documents["docs/BLOCKED_HANDLING.md"] = set.all["docs/BLOCKED_HANDLING.md"]
	require.Greater(t, len(documents), 1, "must scan the full shipped template tree")
	require.Empty(t, undefinedDependencyTypes(documents), "shipped guidance must name only model dependency types")
}

func TestDependencyTypeDocs_BlockedMirrorMatchesGeneratedTemplate(t *testing.T) {
	data := minimalTemplateData()
	data.ProjectPrefix = "PROJ"
	out := t.TempDir()
	gen, err := NewEmbeddedGenerator(out, data, nil)
	require.NoError(t, err)
	_, err = gen.generateFile(DocFile{Name: "BLOCKED_HANDLING.md", TemplateName: "blocked_handling.md.tmpl", Type: TemplateBased}, true)
	require.NoError(t, err)
	generated, err := os.ReadFile(filepath.Join(out, "BLOCKED_HANDLING.md"))
	require.NoError(t, err)
	mirror, err := os.ReadFile(filepath.Join("..", "..", "docs", "BLOCKED_HANDLING.md"))
	require.NoError(t, err)
	require.Equal(t, string(generated), string(mirror))
}
