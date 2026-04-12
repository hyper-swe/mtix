// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/hyper-swe/mtix/internal/model"
)

// separator is the 80-character line dividing node blocks per FR-17.5.
const separator = "================================================================================"

// BriefingOpts controls the briefing renderer per FR-17.4/FR-17.5.
type BriefingOpts struct {
	Fields        []string // Restrict to these fields (nil = default set).
	MaxFieldChars int      // Truncate per-field content (0 = unlimited).
	ShowEmpty     bool     // Include empty/zero fields.
}

// briefingField defines a field to render in the briefing output.
// multiLine controls whether the field uses "LABEL: value" (false) or
// "LABEL:\n  value" (true) format. The order in this slice is the
// stable render order per FR-17.5.
type briefingField struct {
	label     string                      // Display label (uppercase).
	jsonName  string                      // model.Node JSON tag for field validation.
	getter    func(n *model.Node) string  // Extracts the string value.
	multiLine bool                        // Default render mode for this field.
}

// defaultBriefingFields defines the default fields and their order per FR-17.4.
// This order is fixed across releases — new fields are appended at the end.
var defaultBriefingFields = []briefingField{
	{"ID", "id", func(n *model.Node) string { return n.ID }, false},
	{"TITLE", "title", func(n *model.Node) string { return n.Title }, false},
	{"NODE_TYPE", "node_type", func(n *model.Node) string { return string(n.NodeType) }, false},
	{"STATUS", "status", func(n *model.Node) string { return string(n.Status) }, false},
	{"PRIORITY", "priority", func(n *model.Node) string { return fmt.Sprintf("%d", n.Priority) }, false},
	{"ASSIGNEE", "assignee", func(n *model.Node) string { return n.Assignee }, false},
	{"DESCRIPTION", "description", func(n *model.Node) string { return n.Description }, true},
	{"PROMPT", "prompt", func(n *model.Node) string { return n.Prompt }, true},
	{"ACCEPTANCE", "acceptance", func(n *model.Node) string { return n.Acceptance }, true},
}

// RenderBriefing writes node data in the briefing format to w per
// FR-17.4/FR-17.5. Each node is a delimited block with labeled fields.
// Output is streamed line-by-line (not buffered) per FR-17.9.
//
// Control characters are sanitized per FR-17 audit T10: \x00-\x08,
// \x0b-\x1f, \x7f are replaced with U+FFFD. Tab (\x09) and newline
// (\x0a) are preserved. Fields containing newlines are auto-promoted
// to multi-line block format per FR-17 audit T11.
func RenderBriefing(w io.Writer, nodes []*model.Node, opts BriefingOpts) error {
	fields, err := resolveBriefingFields(opts.Fields)
	if err != nil {
		return err
	}

	for _, n := range nodes {
		if _, err := fmt.Fprintln(w, separator); err != nil {
			return err
		}
		if err := renderNodeFields(w, n, fields, opts); err != nil {
			return err
		}
	}

	return nil
}

// renderNodeFields writes one node's fields to w.
func renderNodeFields(w io.Writer, n *model.Node, fields []briefingField, opts BriefingOpts) error {
	for _, f := range fields {
		value := sanitizeControlChars(f.getter(n))

		if opts.MaxFieldChars > 0 && len(value) > opts.MaxFieldChars {
			value = value[:opts.MaxFieldChars] + "...[truncated]"
		}

		if value == "" && !opts.ShowEmpty {
			continue
		}

		if err := writeField(w, f.label, value, f.multiLine); err != nil {
			return err
		}
	}
	return nil
}

// writeField writes a single labeled field to w. Auto-promotes to
// multi-line if the value contains newlines (FR-17 audit T11).
func writeField(w io.Writer, label, value string, forceMulti bool) error {
	isMulti := forceMulti || strings.Contains(value, "\n")

	if !isMulti {
		_, err := fmt.Fprintf(w, "%s: %s\n", label, value)
		return err
	}

	if _, err := fmt.Fprintf(w, "%s:\n", label); err != nil {
		return err
	}
	for _, line := range strings.Split(value, "\n") {
		if _, err := fmt.Fprintf(w, "  %s\n", line); err != nil {
			return err
		}
	}
	return nil
}

// resolveBriefingFields returns the briefing fields to render.
// If fieldNames is nil/empty, returns the default set.
// If fieldNames is specified, validates against the whitelist and
// returns only the matching fields in default order.
func resolveBriefingFields(fieldNames []string) ([]briefingField, error) {
	if len(fieldNames) == 0 {
		return defaultBriefingFields, nil
	}

	// Validate all field names against the model.Node whitelist.
	initNodeFields()
	for _, name := range fieldNames {
		if _, ok := nodeFieldSet[name]; !ok {
			return nil, fmt.Errorf("unknown field %q; valid fields: %s: %w",
				name, strings.Join(ValidFieldNames(), ", "), model.ErrInvalidInput)
		}
	}

	// Build the requested set.
	requested := make(map[string]bool, len(fieldNames))
	for _, name := range fieldNames {
		requested[name] = true
	}

	// Filter defaultBriefingFields to only include requested fields,
	// preserving the stable declaration order.
	var result []briefingField
	for _, f := range defaultBriefingFields {
		if requested[f.jsonName] {
			result = append(result, f)
		}
	}

	// If a requested field is not in defaultBriefingFields (e.g., depth,
	// seq, created_at), we still need to include it. Use a generic
	// string getter via reflection.
	for _, name := range fieldNames {
		found := false
		for _, f := range result {
			if f.jsonName == name {
				found = true
				break
			}
		}
		if !found {
			// Create a generic field using projection.
			jsonName := name
			result = append(result, briefingField{
				label:    strings.ToUpper(jsonName),
				jsonName: jsonName,
				getter: func(n *model.Node) string {
					m, _ := ProjectNode(n, []string{jsonName})
					if v, ok := m[jsonName]; ok {
						return fmt.Sprintf("%v", v)
					}
					return ""
				},
				multiLine: false,
			})
		}
	}

	return result, nil
}

// sanitizeControlChars replaces control characters with U+FFFD per
// FR-17.5 / FR-17 audit T10. Preserves tab (\x09) and newline (\x0a).
func sanitizeControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return '\uFFFD'
		}
		return r
	}, s)
}
