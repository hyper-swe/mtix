// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

// markerPattern matches auto-generated section markers per FR-13.3a.
// Format: <!-- AUTO-GENERATED: SECTION_NAME --> ... <!-- END AUTO-GENERATED -->
var markerPattern = regexp.MustCompile(
	`(?s)(<!-- AUTO-GENERATED: (\w+) -->)(.*?)(<!-- END AUTO-GENERATED -->)`,
)

// updateMarkedSections replaces only auto-generated sections in an existing file.
// Human-edited content outside markers is preserved per FR-13.3a.
// Returns true if any sections were updated.
func updateMarkedSections(
	filePath string,
	templates *template.Template,
	tmplName string,
	data *TemplateData,
) (bool, error) {
	existing, err := os.ReadFile(filePath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", filePath, err)
	}

	// If no markers found, return false (caller should do full generation).
	if !markerPattern.Match(existing) {
		return false, nil
	}

	// A marker without its END (or an END without its open) makes pairing
	// ambiguous, and a rewrite could delete the user text between markers:
	// leave the file untouched. It reports handled so the caller does not
	// regenerate the file whole.
	if name, bad := malformedMarker(string(existing)); bad {
		slog.Warn("auto-generated marker is unterminated; file not modified",
			"file", filepath.Base(filePath), "marker", name)
		return true, nil
	}

	// Render the full template to extract new section content.
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, tmplName, data); err != nil {
		return false, fmt.Errorf("render template %s: %w", tmplName, err)
	}

	// Extract new sections from the rendered template.
	newSections := extractSections(rendered.String())
	if len(newSections) == 0 {
		return false, nil
	}

	// Replace sections in the existing content.
	updated := false
	result := markerPattern.ReplaceAllStringFunc(string(existing), func(match string) string {
		submatch := markerPattern.FindStringSubmatch(match)
		if len(submatch) < 5 {
			return match
		}

		sectionName := submatch[2]
		openTag := submatch[1]
		closeTag := submatch[4]

		newContent, ok := newSections[sectionName]
		if !ok {
			return match
		}

		updated = true
		return openTag + "\n" + newContent + "\n" + closeTag
	})

	// Append every template section whose marker block the file lacks, with
	// the heading that precedes it in the template, after the user's content
	// (an older generated file never gets a newer section otherwise).
	appended := appendMissingSections(result, rendered.String())
	if appended != result {
		updated = true
		result = appended
	}

	if !updated {
		return false, nil
	}

	cleanPath := filepath.Clean(filePath)
	if err := os.WriteFile(cleanPath, []byte(result), 0o644); err != nil { //nolint:gosec // filePath is from internal doc generator, not user input
		return false, fmt.Errorf("write %s: %w", filePath, err)
	}

	return true, nil
}

var (
	openMarkerRE = regexp.MustCompile(`<!-- AUTO-GENERATED: (\w+) -->`)
	endMarker    = "<!-- END AUTO-GENERATED -->"
)

// malformedMarker reports whether text holds an open marker without a
// matching END (or the reverse), and names the first marker that is not
// closed (MTIX-95.8.1).
func malformedMarker(text string) (string, bool) {
	opens := openMarkerRE.FindAllStringSubmatch(text, -1)
	paired := markerPattern.FindAllStringSubmatch(text, -1)
	if len(opens) == len(paired) && strings.Count(text, endMarker) == len(paired) {
		return "", false
	}
	seen := map[string]int{}
	for _, m := range paired {
		seen[m[2]]++
	}
	for _, o := range opens {
		if seen[o[1]] == 0 {
			return o[1], true
		}
		seen[o[1]]--
	}
	return "END", true
}

// appendMissingSections returns existing with a marker block appended, in
// template order, for each section of the rendered template that existing
// does not contain. The block carries the last "## " heading that precedes
// it in the template. Existing content is never changed (MTIX-95.8.1).
func appendMissingSections(existing, rendered string) string {
	have := map[string]bool{}
	for _, m := range markerPattern.FindAllStringSubmatch(existing, -1) {
		have[m[2]] = true
	}
	out := existing
	prev := 0
	for _, loc := range markerPattern.FindAllStringSubmatchIndex(rendered, -1) {
		name := rendered[loc[4]:loc[5]]
		body := strings.TrimSpace(rendered[loc[6]:loc[7]])
		heading := ""
		for _, line := range strings.Split(rendered[prev:loc[0]], "\n") {
			if strings.HasPrefix(line, "## ") {
				heading = line
			}
		}
		prev = loc[1]
		if have[name] || strings.Contains(existing, rendered[loc[2]:loc[3]]) {
			continue
		}
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "\n"
		if heading != "" {
			out += heading + "\n\n"
		}
		out += rendered[loc[2]:loc[3]] + "\n" + body + "\n" + rendered[loc[8]:loc[9]] + "\n"
	}
	return out
}

// extractSections parses marker-delimited sections from rendered template content.
func extractSections(content string) map[string]string {
	sections := make(map[string]string)

	matches := markerPattern.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) >= 4 {
			name := match[2]
			body := strings.TrimSpace(match[3])
			sections[name] = body
		}
	}

	return sections
}

// WrapAutoGenerated wraps content with auto-generated markers.
// Used in templates to mark sections that should be auto-updated.
func WrapAutoGenerated(sectionName, content string) string {
	return fmt.Sprintf(
		"<!-- AUTO-GENERATED: %s -->\n%s\n<!-- END AUTO-GENERATED -->",
		sectionName, content,
	)
}
