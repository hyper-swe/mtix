// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSkillDocFiles_Returns5 verifies the skill file list has all 5 entries.
func TestSkillDocFiles_Returns5(t *testing.T) {
	files := SkillDocFiles()
	assert.Len(t, files, 5)

	names := make(map[string]bool)
	for _, f := range files {
		names[f.Name] = true
		assert.NotEmpty(t, f.TemplateName, "skill %s must have a template", f.Name)
	}

	expected := []string{
		"mtix-task-execution.md",
		"mtix-planning.md",
		"mtix-review.md",
		"mtix-multi-agent.md",
		"mtix-admin.md",
	}
	for _, name := range expected {
		assert.True(t, names[name], "missing skill file: %s", name)
	}
}

// TestReferenceDocFiles_Returns4 verifies the reference file list has all 4 entries.
func TestReferenceDocFiles_Returns4(t *testing.T) {
	files := ReferenceDocFiles()
	assert.Len(t, files, 4)

	names := make(map[string]bool)
	for _, f := range files {
		names[f.Name] = true
		assert.NotEmpty(t, f.TemplateName, "reference %s must have a template", f.Name)
	}

	expected := []string{
		"do-178c-checklist.md",
		"iec-62304-checklist.md",
		"nasa-std-8739-checklist.md",
		"mil-std-498-checklist.md",
	}
	for _, name := range expected {
		assert.True(t, names[name], "missing reference file: %s", name)
	}
}

// TestPluginInstaller_ClaudeCode_Writes5Skills verifies skill files are written.
func TestPluginInstaller_ClaudeCode_Writes5Skills(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	results, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	// Should have 5 skill files + 4 reference files = 9 total.
	assert.Len(t, results, 9)

	// Verify skill files exist in .claude/skills/.
	skillDir := filepath.Join(projectDir, ".claude", "skills")
	assert.DirExists(t, skillDir)

	for _, name := range []string{
		"mtix-task-execution.md",
		"mtix-planning.md",
		"mtix-review.md",
		"mtix-multi-agent.md",
		"mtix-admin.md",
	} {
		assert.FileExists(t, filepath.Join(skillDir, name), "skill %s should exist", name)
	}
}

// TestPluginInstaller_ClaudeCode_WritesReferences verifies reference files are written.
func TestPluginInstaller_ClaudeCode_WritesReferences(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	// Verify reference files in .claude/skills/references/.
	refDir := filepath.Join(projectDir, ".claude", "skills", "references")
	assert.DirExists(t, refDir)

	for _, name := range []string{
		"do-178c-checklist.md",
		"iec-62304-checklist.md",
		"nasa-std-8739-checklist.md",
		"mil-std-498-checklist.md",
	} {
		assert.FileExists(t, filepath.Join(refDir, name), "reference %s should exist", name)
	}
}

// TestPluginInstaller_Idempotent verifies second run produces same result.
func TestPluginInstaller_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	// First run.
	results1, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	// Read a skill file content.
	content1, err := os.ReadFile(filepath.Join(projectDir, ".claude", "skills", "mtix-task-execution.md"))
	require.NoError(t, err)

	// Second run.
	results2, err := installer.Install("claude-code", false)
	require.NoError(t, err)
	assert.Equal(t, len(results1), len(results2))

	// Content should be identical.
	content2, err := os.ReadFile(filepath.Join(projectDir, ".claude", "skills", "mtix-task-execution.md"))
	require.NoError(t, err)
	assert.Equal(t, string(content1), string(content2))
}

// TestPluginInstaller_Global_WritesHomeDir verifies global flag uses home-like dir.
func TestPluginInstaller_Global_WritesHomeDir(t *testing.T) {
	tmpDir := t.TempDir()
	// Set HOME to tmpDir for this test.
	t.Setenv("HOME", tmpDir)

	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	_, err := installer.Install("claude-code", true)
	require.NoError(t, err)

	// Skills should be in ~/.claude/skills/, not project-local.
	globalSkillDir := filepath.Join(tmpDir, ".claude", "skills")
	assert.DirExists(t, globalSkillDir)
	assert.FileExists(t, filepath.Join(globalSkillDir, "mtix-task-execution.md"))
}

// TestPluginInstaller_InvalidTarget_ReturnsError verifies unknown target handling.
func TestPluginInstaller_InvalidTarget_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	data := minimalTemplateData()
	installer := NewPluginInstaller(tmpDir, data, nil)

	_, err := installer.Install("unknown-ide", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported target")
}

// TestSkillContent_ContainsContextChainRule verifies context chain is #1 rule.
func TestSkillContent_ContainsContextChainRule(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	skillDir := filepath.Join(projectDir, ".claude", "skills")

	// Every skill must mention context chain traversal.
	skills := []string{
		"mtix-task-execution.md",
		"mtix-planning.md",
		"mtix-review.md",
		"mtix-multi-agent.md",
		"mtix-admin.md",
	}

	for _, name := range skills {
		content, err := os.ReadFile(filepath.Join(skillDir, name))
		require.NoError(t, err, "reading %s", name)
		assert.Contains(t, strings.ToLower(string(content)), "context",
			"skill %s must mention context chain", name)
	}
}

// TestSkillContent_UsesMCPToolPrefix verifies tools use mcp__mtix__ prefix.
func TestSkillContent_UsesMCPToolPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	// Check task execution skill for mcp__mtix__ prefix in allowed-tools.
	content, err := os.ReadFile(filepath.Join(projectDir, ".claude", "skills", "mtix-task-execution.md"))
	require.NoError(t, err)

	assert.Contains(t, string(content), "mcp__mtix__",
		"allowed-tools must use mcp__mtix__ prefix")
}

// TestSkillContent_HasYAMLFrontmatter verifies YAML frontmatter format.
func TestSkillContent_HasYAMLFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	skillDir := filepath.Join(projectDir, ".claude", "skills")

	skills := []string{
		"mtix-task-execution.md",
		"mtix-planning.md",
		"mtix-review.md",
		"mtix-multi-agent.md",
		"mtix-admin.md",
	}

	for _, name := range skills {
		content, err := os.ReadFile(filepath.Join(skillDir, name))
		require.NoError(t, err, "reading %s", name)
		text := string(content)

		// Must start with YAML frontmatter delimiters.
		assert.True(t, strings.HasPrefix(text, "---\n"),
			"skill %s must start with YAML frontmatter (---)", name)
		// Must have closing frontmatter.
		assert.True(t, strings.Count(text, "---\n") >= 2,
			"skill %s must have closing YAML frontmatter (---)", name)
		// Must contain description field.
		assert.Contains(t, text, "description:",
			"skill %s must have description in frontmatter", name)
		// Must contain allowed-tools field.
		assert.Contains(t, text, "allowed-tools:",
			"skill %s must have allowed-tools in frontmatter", name)
	}
}

// TestSkillContent_ContainsSafetyCriticalProcedures verifies safety-critical baseline.
func TestSkillContent_ContainsSafetyCriticalProcedures(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	// Task execution skill must contain safety-critical procedures.
	content, err := os.ReadFile(filepath.Join(projectDir, ".claude", "skills", "mtix-task-execution.md"))
	require.NoError(t, err)
	text := strings.ToLower(string(content))

	assert.Contains(t, text, "verification",
		"task execution skill must mention verification")
	assert.Contains(t, text, "traceability",
		"task execution skill must mention traceability")
	assert.Contains(t, text, "acceptance criteria",
		"task execution skill must mention acceptance criteria")
}

// TestSkillContent_TaskExecution_HasExecutionProtocol verifies step-by-step protocol.
func TestSkillContent_TaskExecution_HasExecutionProtocol(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(projectDir, ".claude", "skills", "mtix-task-execution.md"))
	require.NoError(t, err)
	text := string(content)

	// Must contain the key workflow steps.
	assert.Contains(t, text, "session",
		"must mention session lifecycle")
	assert.Contains(t, text, "heartbeat",
		"must mention heartbeat protocol")
	assert.Contains(t, text, "mtix_context",
		"must reference mtix_context tool")
	assert.Contains(t, text, "mtix_claim",
		"must reference mtix_claim tool")
	assert.Contains(t, text, "mtix_done",
		"must reference mtix_done tool")
}

// TestSkillContent_Planning_HasDecompositionRules verifies decomposition guidance.
func TestSkillContent_Planning_HasDecompositionRules(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(projectDir, ".claude", "skills", "mtix-planning.md"))
	require.NoError(t, err)
	text := strings.ToLower(string(content))

	assert.Contains(t, text, "decompos",
		"planning skill must mention decomposition")
	assert.Contains(t, text, "prompt",
		"planning skill must mention prompt field")
	assert.Contains(t, text, "acceptance",
		"planning skill must mention acceptance field")
}

// TestReferenceContent_ContainsStandardName verifies references mention their standard.
func TestReferenceContent_ContainsStandardName(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	installer := NewPluginInstaller(projectDir, data, nil)

	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	refDir := filepath.Join(projectDir, ".claude", "skills", "references")

	tests := []struct {
		file     string
		contains string
	}{
		{"do-178c-checklist.md", "DO-178C"},
		{"iec-62304-checklist.md", "IEC 62304"},
		{"nasa-std-8739-checklist.md", "NASA"},
		{"mil-std-498-checklist.md", "MIL-STD-498"},
	}

	for _, tt := range tests {
		content, err := os.ReadFile(filepath.Join(refDir, tt.file))
		require.NoError(t, err, "reading %s", tt.file)
		assert.Contains(t, string(content), tt.contains,
			"reference %s must mention %s", tt.file, tt.contains)
	}
}

// TestSkillContent_ProjectPrefix_Substituted verifies project prefix rendering.
func TestSkillContent_ProjectPrefix_Substituted(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	data := minimalTemplateData()
	data.ProjectPrefix = "SATURN"
	installer := NewPluginInstaller(projectDir, data, nil)

	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(projectDir, ".claude", "skills", "mtix-task-execution.md"))
	require.NoError(t, err)

	assert.Contains(t, string(content), "SATURN",
		"skill should contain the project prefix")
}
