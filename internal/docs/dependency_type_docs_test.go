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
	plainProse := `(?i:\b(?:use|uses|with|via))[ \t]+([A-Za-z][A-Za-z0-9_-]*)[ \t]+(?:dependency|dependencies)\b`
	patterns := []string{
		quoted + `\s+(?:dependency|dep)\s+types?\b`,
		`(?i:\b(?:use|uses|with|via|the|an?))\s+` + named + `\s+(?:dependency|dep)\s+types?\b`,
		quoted + `\s+(?:dependency|dependencies|dep)\b`,
		plainProse,
		`(?i:\b(?:dependency|dep)\s+(?:of\s+)?types?)\s+` + quoted,
		`(?i:\b(?:dependency|dep)\s+(?:of\s+)?types?)\s+` + named + `(?:[.,;!?)]|\s*$|\s+(?:for|to|with|as|when|if|means|indicates|represents)\b)`,
		`(?i:\b(?:dependency|dep)\s+(?:of\s+)?types?)\s*(?::|=|\bis\b|\bare\b|\bnamed\b|\bcalled\b|\bof\b|\binclude[s]?\b)\s*` + named,
		`(?:mtix\s+dep\s+(?:add|remove)|(?:mcp__mtix__)?mtix_dep_(?:add|remove))\b[^\n` + "`" + `]*?--type(?:=|\s+)` + named,
		"[`\"']?dep_type[`\"']?\\s*[:=]\\s*" + named,
	}
	var names []string
	normalized := normalizeDependencyMarkup(strings.ReplaceAll(body, "\\\n", " "))
	for _, pattern := range patterns {
		for _, match := range regexp.MustCompile(pattern).FindAllStringSubmatch(normalized, -1) {
			if pattern != plainProse || !unnamedDependencyDeterminer(match[1]) {
				names = append(names, match[1])
			}
		}
	}
	names = append(names, dependencyListMentions(normalized)...)
	names = append(names, dependencyProseLists(normalized)...)
	return names
}

// Unquoted articles/determiners describe dependencies without naming a kind.
// A value actually added to the model remains recognizable even in this form.
func unnamedDependencyDeterminer(name string) bool {
	if name != "a" && name != "an" && name != "the" && name != "no" {
		return false
	}
	for _, kind := range model.AllDepTypes() {
		if name == string(kind) {
			return false
		}
	}
	return true
}

func normalizeDependencyMarkup(body string) string {
	asterisks := regexp.MustCompile("\\*{1,3}([`]?\\b[A-Za-z][A-Za-z0-9_-]*[`]?)\\*{1,3}")
	underscores := regexp.MustCompile(`(^|[^A-Za-z0-9_])_{1,3}([A-Za-z][A-Za-z0-9_-]*?)_{1,3}([^A-Za-z0-9_]|$)`)
	body = asterisks.ReplaceAllString(body, "$1")
	for {
		next := underscores.ReplaceAllString(body, "${1}${2}${3}")
		if next == body {
			return body
		}
		body = next
	}
}

func dependencyProseLists(body string) []string {
	name := "[`\"']?[A-Za-z][A-Za-z0-9_-]*[`\"']?"
	separator := `\s*(?:,\s*(?:\band\b|\bor\b)?|\band\b|\bor\b|/)\s*`
	list := name + "(?:" + separator + name + ")+"
	patterns := []string{
		"(" + list + `)\s+(?:dependency|dep)\s+types?\b`,
		`(?i:\b(?:dependency|dep)\s+types?)\s*(?::|=|\bare\b|\bis\b|\binclude[s]?\b|\bnamed\b|\bcalled\b|\bof\b)\s*(` + name + "(?:" + separator + name + ")*)",
		`(?i:\b(?:dependency|dep)\s+types?)\s+(` + list + ")",
	}
	var names []string
	for _, pattern := range patterns {
		for _, match := range regexp.MustCompile(pattern).FindAllStringSubmatch(body, -1) {
			names = append(names, dependencyNameList(match[1])...)
		}
	}
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
			valueLine := line
			if match := intro.FindStringSubmatch(line); match != nil {
				valueLine = match[1]
			}
			valueLine = strings.SplitN(valueLine, ":", 2)[0]
			for _, match := range literal.FindAllStringSubmatch(valueLine, -1) {
				names = append(names, match[1])
			}
		}
		if match := intro.FindStringSubmatch(line); match != nil {
			inTypes = true
			names = append(names, dependencyNameList(match[1])...)
		}
		if strings.Contains(line, "|") {
			var cellNames []string
			cellNames, column = dependencyTableNames(line, inTypes, column)
			names = append(names, cellNames...)
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

// A colon ends the named-value portion; conjunctions, commas and slashes
// after it belong to the description, not to the dependency type list.
func dependencyNameList(text string) []string {
	text = strings.SplitN(text, ":", 2)[0]
	separator := regexp.MustCompile(`\s*(?:,\s*(?:\band\b|\bor\b)?|\band\b|\bor\b|/)\s*`)
	firstName := regexp.MustCompile("^[`*\"']*([A-Za-z][A-Za-z0-9_-]*)")
	var names []string
	for _, part := range separator.Split(text, -1) {
		if match := firstName.FindStringSubmatch(strings.TrimSpace(part)); match != nil {
			names = append(names, match[1])
		}
	}
	return names
}

func dependencyTableNames(line string, inTypes bool, column int) ([]string, int) {
	cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for i, cell := range cells {
		label := strings.ToLower(strings.Trim(strings.TrimSpace(cell), "`*_ "))
		if label == "dependency type" || label == "dependency types" || label == "dep type" || label == "dep types" || label == "dep_type" || (inTypes && label == "type") {
			return nil, i
		}
	}
	if column < 0 || column >= len(cells) {
		return nil, column
	}
	return dependencyNameList(cells[column]), column
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
		{"article", "Use a dependency for ordering.", false},
		{"determiner", "Tasks with no dependency annotations need attention.", false},
		{"ordinary description", "The external dependency is unresolved.", false},
		{"CLI description", "Use the same type with `mtix dep remove`.", false},
		{"issue create", "mtix create --type feature", false},
		{"emphasized issue types", "**Issue types** are bug and feature. `mtix create --type feature`", false},
		{"issue table", "| Issue type | Purpose |\n| --- | --- |\n| **bug**, *feature* | Work |", false},
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

func TestDependencyTypeDocs_EmphasisAndAllNamedValues(t *testing.T) {
	cases := []struct{ name, body string }{
		{"bold", "Use **future_kind** dependency type"},
		{"emphasis", "Use *future_kind* dependency type"},
		{"underscore bold", "Use __future_kind__ dependency type"},
		{"underscore emphasis", "Use _future_kind_ dependency type"},
		{"bold code", "Use **`future_kind`** dependency type"},
		{"later table name", "| Dependency type | Purpose |\n| --- | --- |\n| blocks, future_kind | Request |"},
		{"later table name with markup", "| Dependency type | Purpose |\n| --- | --- |\n| **blocks** and _future_kind_ | Request |"},
		{"middle table name", "| Dependency type | Purpose |\n| --- | --- |\n| blocks, future_kind, related | Request |"},
		{"plural before", "Use blocks and future_kind dependency types."},
		{"plural after", "The dependency types are blocks and future_kind."},
		{"plural both unknown positions", "Use future_kind, blocks and other_future dependency types."},
		{"Oxford comma", "Use blocks, related, and future_kind dependency types."},
		{"adjacent underscore names", "Use __blocks__/__future_kind__ dependency types."},
		{"plural markup", "Use **blocks** and *future_kind* dependency types."},
		{"plural examples", "Dependency types include blocks, related and future_kind."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := undefinedDependencyTypes(map[string]string{"x.tmpl": tc.body})
			require.NotEmpty(t, hits, tc.body)
			require.Contains(t, strings.Join(hits, "\n"), "future_kind", "must report the named invalid value")
			if strings.Contains(tc.body, "other_future") {
				require.Contains(t, strings.Join(hits, "\n"), "other_future", "must report every invalid value")
			}
		})
	}
}

func TestDependencyTypeDocs_EveryModelValueIsAccepted(t *testing.T) {
	for _, kind := range model.AllDepTypes() {
		t.Run(string(kind), func(t *testing.T) {
			name := string(kind)
			forms := []string{
				"Use `" + name + "` dependency type.",
				"Use **" + name + "** dependency type.",
				"Use *" + name + "* dependency type.",
				"Use __" + name + "__ dependency type.",
				"Use _" + name + "_ dependency type.",
				"Use **`" + name + "`** dependency type.",
				"Use blocks and " + name + " dependency types.",
				"The dependency types are blocks and " + name + ".",
				"Use blocks, related, and " + name + " dependency types.",
				"Use __blocks__/__" + name + "__ dependency types.",
				"| Dependency type | Purpose |\n| --- | --- |\n| blocks, " + name + " | Supported |",
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

func TestDependencyTypeDocs_ReviewMatrix(t *testing.T) {
	cases := []struct {
		name, body string
		invalid    bool
	}{
		{"bold", "Use **r2_invalid** dependency type.", true},
		{"table multiple", "| Dependency type | Meaning |\n| --- | --- |\n| blocks, r2_invalid | request |", true},
		{"plural before", "Use blocks and r2_invalid dependency types.", true},
		{"plural after", "The dependency types are blocks and r2_invalid.", true},
		{"plain dependency", "Use r2_invalid dependency for requests.", true},
		{"plain dependencies", "Use r2_invalid dependencies for requests.", true},
		{"underscore code", "Use _`r2_invalid`_ dependency type.", true},
		{"underscore bold code", "Use __`r2_invalid`__ dependency type.", true},
		{"italic table header", "| _Dependency type_ | Meaning |\n| --- | --- |\n| r2_invalid | request |", true},
		{"bold underscore table header", "| __Dependency types__ | Meaning |\n| --- | --- |\n| blocks and r2_invalid | request |", true},
		{"italic code table header", "| _`dep_type`_ | Meaning |\n| --- | --- |\n| r2_invalid | request |", true},
		{"bullet and description", "Dependency types:\n- blocks: prevents work and supports ordering", false},
		{"bullet slash description", "Dependency types:\n- related: informational/context link", false},
		{"bullet comma description", "Dependency types:\n- duplicates: identifies repeated work, with matching requirements", false},
		{"valid table descriptions", "| Dependency type | Meaning |\n| --- | --- |\n| blocks | hard and required |", false},
		{"ordinary issue", "Use **bug** issue type and _feature_ issue type.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits := undefinedDependencyTypes(map[string]string{"matrix.tmpl": c.body})
			require.Equal(t, c.invalid, len(hits) > 0, "%v", hits)
		})
	}
}

func TestDependencyTypeDocs_ModelHeaderAndDescriptionMatrix(t *testing.T) {
	for _, kind := range model.AllDepTypes() {
		name := string(kind)
		bodies := []string{"Use " + name + " dependency for requests.", "Use " + name + " dependencies for requests."}
		for _, header := range []string{"Dependency type", "*Dependency type*", "**Dependency types**", "_Dependency type_", "__Dependency types__", "`Dependency type`", "_`dep_type`_", "__`dep_type`__"} {
			bodies = append(bodies, "| "+header+" | Meaning |\n| --- | --- |\n| "+name+" | request |")
		}
		for _, description := range []string{"prevents work and supports ordering", "information/context link", "repeated work, with matching requirements", "work or information", "supports `task` ordering and context"} {
			bodies = append(bodies, "Dependency types: "+name+": "+description, "Dependency types:\n- "+name+": "+description,
				"Dependency types:\n- blocks, "+name+": "+description,
				"| _Dependency type_ | Meaning |\n| --- | --- |\n| blocks, "+name+": "+description+" | request |")
			hits := undefinedDependencyTypes(map[string]string{"x.tmpl": "Dependency types:\n- blocks, future_kind: " + description})
			require.Contains(t, strings.Join(hits, "\n"), "future_kind")
		}
		for _, body := range bodies {
			require.Contains(t, dependencyTypeMentions(body), name, body)
			require.Empty(t, undefinedDependencyTypes(map[string]string{"x.tmpl": body}), body)
		}
	}
}
