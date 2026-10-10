// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests shipped launch guidance and rendered instruction coverage.
package docs

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var wakeLaunchRE = regexp.MustCompile(`\b(?:claude\s+(?:-p|--print)|codex\s+exec|agent\s+(?:-p|--print))\b`)
var wakeOperandRE = regexp.MustCompile(`\b(?:claude[ \t]+(?:-p|--print)|codex[ \t]+exec|agent[ \t]+(?:-p|--print))\b([^\n\x60]*)`)

func wakeLaunchProblems(text string) []string {
	text = strings.ReplaceAll(text, "\\\n", " ")
	var problems []string
	for _, match := range wakeOperandRE.FindAllStringSubmatch(text, -1) {
		tail := strings.TrimSpace(match[1])
		// Static support-table names have no command operands.
		if tail == "" {
			continue
		}
		if tail != "-" {
			problems = append(problems, match[0])
		}
	}
	return problems
}

func wakeShippedFiles(t *testing.T) map[string]string {
	t.Helper()
	files := loadShippedDocs(t).all
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(filepath.Join(root, "examples"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	require.NoError(t, err)
	require.Contains(t, files, "examples/hooks/wake-agent.sh")
	require.Contains(t, files, "USERMANUAL.md")
	return files
}

func TestWakeExample_ShippedGuidance_UsesStaticLaunchOperands(t *testing.T) {
	for path, text := range wakeShippedFiles(t) {
		t.Run(path, func(t *testing.T) { require.Empty(t, wakeLaunchProblems(text)) })
	}
}

func TestWakeExample_CommandForms_AreClassifiedIndependently(t *testing.T) {
	tests := []struct {
		text  string
		valid bool
	}{
		{`printf '%s\n' "$INPUT" | claude -p`, true},
		{`# printf '%s\n' "$MESSAGE" | codex exec -`, true},
		{`claude -p "$INPUT"`, false},
		{`claude --print "${OTHER_INPUT}"`, false},
		{`# codex exec "${MESSAGE}"`, false},
		{"codex exec \\\n  \"$OTHER\"", false},
		{"Use `agent -p \"$TEXT\"`.", false},
		{`claude -p 'literal input'`, false},
		{`codex exec "$(cat input)"`, false},
		{`codex exec ordinary-input`, false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) { require.Equal(t, tt.valid, len(wakeLaunchProblems(tt.text)) == 0) })
	}
}

func TestWakeExample_GeneratedGuidance_DescribesRoutine(t *testing.T) {
	paths := []string{"internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md"}
	for _, path := range paths {
		text := wakeSource(t, path)
		for _, term := range []string{"standard input", "empty", "ack", "wake-agent.sh"} {
			require.Contains(t, text, term, path)
		}
	}
	for _, path := range []string{"internal/docs/templates/agents.md.tmpl", "internal/docs/templates/claude.md.tmpl"} {
		require.Contains(t, wakeSource(t, path), "wake-agent.sh", path)
	}
}

func wakeRoutine(t *testing.T, text string) string {
	t.Helper()
	parts := strings.SplitN(text, "## Reference Wake Routine\n", 2)
	require.Len(t, parts, 2)
	return strings.TrimSpace(strings.SplitN(parts[1], "<!-- END AUTO-GENERATED -->", 2)[0])
}

func TestWakeExample_RenderedGuidance_MatchesShippedRoutine(t *testing.T) {
	out := t.TempDir()
	data := minimalTemplateData()
	installer := NewPluginInstaller(out, data, nil)
	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)
	generated, err := os.ReadFile(filepath.Join(out, ".claude", "skills", "mtix-multi-agent.md"))
	require.NoError(t, err)
	want := wakeRoutine(t, string(generated))
	for _, path := range []string{".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md"} {
		require.Equal(t, want, wakeRoutine(t, wakeSource(t, path)), path)
	}
	_, err = installer.Install("codex", false)
	require.NoError(t, err)
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		b, err := os.ReadFile(filepath.Join(out, name))
		require.NoError(t, err)
		require.Contains(t, string(b), "standard-input launch routine")
	}
}

func TestWakeExample_ManagedGuidance_RefreshesExistingSection(t *testing.T) {
	out := t.TempDir()
	gen, err := NewEmbeddedGenerator(out, minimalTemplateData(), nil)
	require.NoError(t, err)
	for _, name := range []string{"agents", "claude"} {
		file := strings.ToUpper(name) + ".md"
		require.NoError(t, os.WriteFile(filepath.Join(out, file), []byte("User notes\n<!-- AUTO-GENERATED: WAKE_ROUTINE -->\nPrevious routine\n<!-- END AUTO-GENERATED -->\n"), 0o600))
		_, err = gen.generateFile(DocFile{Name: file, TemplateName: name + ".md.tmpl", Type: TemplateBased}, true)
		require.NoError(t, err)
		b, err := os.ReadFile(filepath.Join(out, file))
		require.NoError(t, err)
		require.Contains(t, string(b), "User notes")
		require.Contains(t, string(b), "standard-input launch routine")
		require.NotContains(t, string(b), "Previous routine")
	}
}
