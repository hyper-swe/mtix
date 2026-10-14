// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// prepareProject configures a generator copy without mutating caller data.
func (g *Generator) prepareProject() ([]GenerateResult, error) {
	if g.projectDir == "" {
		return nil, nil
	}
	rootData, referenceData, pathErr := projectTemplateData(g.projectDir, g.outputDir, g.data)
	if pathErr != nil {
		return nil, fmt.Errorf("prepare project placement: %w", pathErr)
	}
	results, generateErr := generateProjectBriefing(g.projectDir, g.templates, rootData)
	if generateErr != nil {
		return nil, fmt.Errorf("generate project briefing: %w", generateErr)
	}
	g.data = referenceData
	return results, nil
}

// projectTemplateData locates the briefing and references independently (FR-13.2).
func projectTemplateData(root, output string, data *TemplateData) (*TemplateData, *TemplateData, error) {
	if data == nil {
		return nil, nil, fmt.Errorf("briefing data is required")
	}
	rootPath, rootErr := filepath.Abs(root)
	if rootErr != nil {
		return nil, nil, fmt.Errorf("resolve project root: %w", rootErr)
	}
	outputPath, outputErr := filepath.Abs(output)
	if outputErr != nil {
		return nil, nil, fmt.Errorf("resolve reference output: %w", outputErr)
	}
	if placementErr := validateProjectPlacement(rootPath, outputPath); placementErr != nil {
		return nil, nil, fmt.Errorf("validate project placement: %w", placementErr)
	}
	docsPath, docsErr := filepath.Rel(rootPath, outputPath)
	if docsErr != nil {
		return nil, nil, fmt.Errorf("locate project references: %w", docsErr)
	}
	briefingPath, briefingErr := filepath.Rel(outputPath, filepath.Join(rootPath, "MTIX.md"))
	if briefingErr != nil {
		return nil, nil, fmt.Errorf("locate project briefing: %w", briefingErr)
	}
	rootData, referenceData := *data, *data
	rootData.DocsPath = markdownProjectPath(docsPath) + "/"
	rootData.BriefingPath = "MTIX.md"
	referenceData.DocsPath = ""
	referenceData.BriefingPath = markdownProjectPath(briefingPath)
	return &rootData, &referenceData, nil
}

func validateProjectPlacement(root, output string) error {
	if root == output {
		return fmt.Errorf("reference output directory is the project root; choose a separate reference directory")
	}
	rootInfo, rootErr := os.Stat(root)
	if rootErr != nil {
		return fmt.Errorf("inspect project root: %w", rootErr)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("project root is not a directory")
	}
	outputInfo, outputErr := os.Stat(output)
	if os.IsNotExist(outputErr) {
		return nil
	}
	if outputErr != nil {
		return fmt.Errorf("inspect reference output: %w", outputErr)
	}
	if os.SameFile(rootInfo, outputInfo) {
		return fmt.Errorf("reference output directory is the project root; choose a separate reference directory")
	}
	if !outputInfo.IsDir() {
		return fmt.Errorf("reference output is not a directory")
	}
	return nil
}

// markdownProjectPath escapes each segment while keeping relative separators.
func markdownProjectPath(path string) string {
	segments := strings.Split(filepath.ToSlash(path), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}
