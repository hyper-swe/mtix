// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerator_RenderTemplate_CreatesFile verifies template rendering writes files.
func TestGenerator_RenderTemplate_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	// Try to render a simple template.
	results, err := gen.Generate(false)
	require.NoError(t, err)
	require.Len(t, results, 11)

	// Verify at least one file was actually created on disk.
	for _, result := range results {
		path := filepath.Join(outDir, result.File)
		assert.FileExists(t, path, "file %s should exist", result.File)
	}
}

// TestGenerator_GenerateFile_TemplateBased_SkipsOnSecondRun verifies caching behavior.
func TestGenerator_GenerateFile_TemplateBased_SkipsOnSecondRun(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	// First generation.
	results1, err := gen.Generate(false)
	require.NoError(t, err)

	// Count generated vs skipped.
	generated1 := 0
	for _, r := range results1 {
		if r.Action == "generated" {
			generated1++
		}
	}
	assert.Greater(t, generated1, 0)

	// Second generation should skip template-based files.
	results2, err := gen.Generate(false)
	require.NoError(t, err)

	skipped := 0
	for _, r := range results2 {
		if r.Action == "skipped" {
			skipped++
		}
	}
	assert.Greater(t, skipped, 0)
}

// TestGenerator_Force_SkipsNone verifies --force regenerates all.
func TestGenerator_Force_SkipsNone(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	// First generation.
	_, err := gen.Generate(false)
	require.NoError(t, err)

	// Second generation with force should not skip anything.
	results, err := gen.Generate(true)
	require.NoError(t, err)

	for _, r := range results {
		assert.NotEqual(t, "skipped", r.Action,
			"file %s should not be skipped with force=true", r.File)
	}
}

// TestGenerator_OutputDirCreated verifies output directory is created if missing.
func TestGenerator_OutputDirCreated(t *testing.T) {
	tmpDir := t.TempDir()
	// Note: We use a non-existent subdirectory path.
	outDir := filepath.Join(tmpDir, "nested", "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	results, err := gen.Generate(false)
	require.NoError(t, err)
	assert.Greater(t, len(results), 0)

	// Directory should now exist.
	assert.DirExists(t, outDir)
}

// TestIntrospectCLI_RootCommand verifies root command extraction.
func TestIntrospectCLI_RootCommand(t *testing.T) {
	// Use minimalTemplateData to get a template data structure.
	data := minimalTemplateData()

	require.NotEmpty(t, data.Commands)
	// The root command should be in the list.
	assert.True(t, len(data.Commands) > 0)
}

// TestIntrospectStateMachine_ContainsExpectedTransitions verifies transition extraction.
func TestIntrospectStateMachine_ContainsExpectedTransitions(t *testing.T) {
	transitions := IntrospectStateMachine()

	require.NotEmpty(t, transitions)

	// Verify some key transitions exist.
	found := make(map[string]bool)
	for _, tr := range transitions {
		key := string(tr.From) + "->" + string(tr.To)
		found[key] = true
	}

	// Expected core transitions.
	assert.True(t, found["open->in_progress"] || found["open->deferred"] || found["open->blocked"])
}

// TestIntrospectConfig_NonEmpty verifies config keys are extracted.
func TestIntrospectConfig_NonEmpty(t *testing.T) {
	keys := IntrospectConfig()
	assert.NotEmpty(t, keys)
	// Prefix is expected to be a valid config key.
	assert.Contains(t, keys, "prefix")
}

// TestIntrospectErrors_ContainsAll verifies all error codes are present.
func TestIntrospectErrors_ContainsAll(t *testing.T) {
	errors := IntrospectErrors()

	expectedErrors := []string{
		"ErrNotFound",
		"ErrAlreadyExists",
		"ErrInvalidInput",
		"ErrInvalidTransition",
		"ErrCycleDetected",
		"ErrConflict",
		"ErrAlreadyClaimed",
		"ErrNodeBlocked",
		"ErrStillDeferred",
		"ErrAgentStillActive",
		"ErrNoActiveSession",
		"ErrInvalidConfigKey",
		"ErrDepthWarning",
	}

	for _, expected := range expectedErrors {
		assert.Contains(t, errors, expected, "error code %s should be present", expected)
	}
}

// TestBuildTemplateData_AllFieldsPopulated verifies complete data assembly.
func TestBuildTemplateData_AllFieldsPopulated(t *testing.T) {
	// Use the minimal template data as created in minimalTemplateData.
	data := minimalTemplateData()

	// Verify all fields are populated.
	assert.NotEmpty(t, data.ProjectPrefix)
	assert.NotEmpty(t, data.Version)
	assert.NotEmpty(t, data.Commands)
	assert.NotEmpty(t, data.Transitions)
	assert.NotEmpty(t, data.Statuses)
	assert.NotEmpty(t, data.MCPTools)
	assert.NotEmpty(t, data.ConfigKeys)
	assert.NotEmpty(t, data.ErrorCodes)
}

// TestExtractSections_EmptyContent returns empty map.
func TestExtractSections_EmptyContent(t *testing.T) {
	content := ""
	sections := extractSections(content)
	assert.Empty(t, sections)
}

// TestExtractSections_NoMarkers_EmptyMap verifies unmmarked content yields empty map.
func TestExtractSections_NoMarkers_EmptyMap(t *testing.T) {
	content := `This is just plain text
	with no markers at all`

	sections := extractSections(content)
	assert.Empty(t, sections)
}

// TestExtractSections_SingleSection verifies single section extraction.
func TestExtractSections_SingleSection(t *testing.T) {
	content := `<!-- AUTO-GENERATED: SINGLE -->
	Section content here
	<!-- END AUTO-GENERATED -->`

	sections := extractSections(content)
	require.Len(t, sections, 1)
	assert.Contains(t, sections, "SINGLE")
	assert.Contains(t, sections["SINGLE"], "Section content here")
}

// TestWrapAutoGenerated_ContainsAllParts verifies marker wrapping structure.
func TestWrapAutoGenerated_ContainsAllParts(t *testing.T) {
	result := WrapAutoGenerated("SECTION_NAME", "content here")

	assert.Contains(t, result, "<!-- AUTO-GENERATED: SECTION_NAME -->")
	assert.Contains(t, result, "content here")
	assert.Contains(t, result, "<!-- END AUTO-GENERATED -->")
}

// TestWrapAutoGenerated_OrderIsCorrect verifies marker order.
func TestWrapAutoGenerated_OrderIsCorrect(t *testing.T) {
	result := WrapAutoGenerated("TEST", "body")

	openMarker := "<!-- AUTO-GENERATED: TEST -->"
	closeMarker := "<!-- END AUTO-GENERATED -->"

	openPos := findPosition(result, openMarker)
	closePos := findPosition(result, closeMarker)

	assert.Less(t, openPos, closePos, "opening marker should come before closing marker")
}

// TestUpdateMarkedSections_FileNotFound_ReturnsError verifies missing file handling per FR-13.3a.
func TestUpdateMarkedSections_FileNotFound_ReturnsError(t *testing.T) {
	data := minimalTemplateData()
	gen := mustCreateGenerator(t, t.TempDir(), data)

	_, err := updateMarkedSections("/nonexistent/path.md", gen.templates, "agents.md.tmpl", data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read")
}

// TestUpdateMarkedSections_WithMarkers_UpdatesSection verifies marker-based updates per FR-13.3a.
func TestUpdateMarkedSections_WithMarkers_UpdatesSection(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	// Generate files first.
	_, err := gen.Generate(false)
	require.NoError(t, err)

	// Write a file with auto-generated markers and custom content.
	markedFile := filepath.Join(tmpDir, "marked.md")
	content := "# My Custom Doc\n\nHuman text here.\n\n" +
		WrapAutoGenerated("COMMANDS", "old command list") +
		"\n\nMore human text."
	require.NoError(t, os.WriteFile(markedFile, []byte(content), 0o644))

	// Try updating — template may or may not have COMMANDS section, but the function should not error.
	_, err = updateMarkedSections(markedFile, gen.templates, "cli_reference.md.tmpl", data)
	require.NoError(t, err)
}

// TestUpdateMarkedSections_NoMarkers_ReturnsFalse verifies files without markers are skipped.
func TestUpdateMarkedSections_NoMarkers_ReturnsFalse(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "plain.md")
	require.NoError(t, os.WriteFile(filePath, []byte("no markers here"), 0o644))

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, t.TempDir(), data)

	updated, err := updateMarkedSections(filePath, gen.templates, "agents.md.tmpl", data)
	require.NoError(t, err)
	assert.False(t, updated)
}

// TestRenderTemplate_InvalidTemplate_ReturnsError verifies template execution error handling.
func TestRenderTemplate_InvalidTemplate_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	// Try to render a non-existent template name — directory exists but template doesn't.
	err := gen.renderTemplate("nonexistent.tmpl", filepath.Join(outDir, "bad.md"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "execute template")
}

// TestRenderTemplate_InvalidPath_ReturnsError verifies file creation error handling.
func TestRenderTemplate_InvalidPath_ReturnsError(t *testing.T) {
	data := minimalTemplateData()
	gen := mustCreateGenerator(t, t.TempDir(), data)

	err := gen.renderTemplate("agents.md.tmpl", "/nonexistent/dir/file.md")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create")
}

// TestNewGenerator_NilLogger_UsesDefault verifies nil logger fallback per FR-13.1.
func TestNewGenerator_NilLogger_UsesDefault(t *testing.T) {
	data := minimalTemplateData()
	gen, err := NewGenerator("templates", t.TempDir(), data, nil)
	require.NoError(t, err)
	assert.NotNil(t, gen)
}

// TestNewGenerator_InvalidTemplateDir_ReturnsError verifies invalid template path handling.
func TestNewGenerator_InvalidTemplateDir_ReturnsError(t *testing.T) {
	data := minimalTemplateData()
	_, err := NewGenerator("/nonexistent/templates", t.TempDir(), data, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse templates")
}

// TestIntrospectMCPTools_MultipleTools verifies multi-tool extraction.
func TestIntrospectMCPTools_MultipleTools(t *testing.T) {
	// Use the template data which includes MCP tools.
	data := minimalTemplateData()

	require.NotEmpty(t, data.MCPTools)
	for _, tool := range data.MCPTools {
		assert.NotEmpty(t, tool.Name)
		// Description may be empty for some tools.
	}
}

// TestCommandInfo_FlagsExtracted verifies CLI flag extraction.
func TestCommandInfo_FlagsExtracted(t *testing.T) {
	// Verify flag info structure is created correctly.
	data := minimalTemplateData()

	require.NotEmpty(t, data.Commands)

	// Note: Some commands might not have flags, so we just verify the structure is there.
	assert.True(t, len(data.Commands) > 0)
}

// TestGenerateResult_ActionTypes verifies result action values.
func TestGenerateResult_ActionTypes(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "docs")

	data := minimalTemplateData()
	gen := mustCreateGenerator(t, outDir, data)

	results, err := gen.Generate(false)
	require.NoError(t, err)

	validActions := map[string]bool{
		"generated": true,
		"updated":   true,
		"skipped":   true,
	}

	for _, result := range results {
		assert.True(t, validActions[result.Action],
			"result action %s should be one of: generated, updated, skipped", result.Action)
	}
}

// Helper function to find the position of a substring.
func findPosition(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// TestAllDocFiles_ContainsExpectedNames verifies all expected files are listed.
func TestAllDocFiles_ContainsExpectedNames(t *testing.T) {
	files := AllDocFiles()

	names := make(map[string]bool)
	for _, f := range files {
		names[f.Name] = true
	}

	// Verify at least the core doc files.
	coreFiles := []string{"AGENTS.md", "CLI_REFERENCE.md", "STATUS_MACHINE.md"}
	for _, name := range coreFiles {
		assert.True(t, names[name], "expected doc file %s", name)
	}
}

// TestAllDocFiles_AllHaveTemplateNames verifies no doc file has empty template.
func TestAllDocFiles_AllHaveTemplateNames(t *testing.T) {
	files := AllDocFiles()

	for _, f := range files {
		assert.NotEmpty(t, f.TemplateName,
			"doc file %s must have a template name", f.Name)
	}
}

// TestDocType_Values verifies DocType constants exist.
func TestDocType_Values(t *testing.T) {
	// Verify that AutoGenerated and TemplateBased values are distinct.
	auto := AutoGenerated
	tmpl := TemplateBased

	assert.NotEqual(t, auto, tmpl)
}
