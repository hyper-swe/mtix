// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// CheckProjectBriefing compares FR-13 project instructions with this generator's
// exact rendering. It returns advisory reasons without writing any file.
func (g *Generator) CheckProjectBriefing(root string) ([]string, error) {
	rootData, _, err := projectTemplateData(root, g.outputDir, g.data)
	if err != nil {
		return nil, fmt.Errorf("prepare expected project placement: %w", err)
	}
	expected, err := renderProjectBriefing(g.templates, rootData)
	if err != nil {
		return nil, fmt.Errorf("render expected project briefing: %w", err)
	}
	actual, exists, err := readRegularDoc(filepath.Join(root, "MTIX.md"))
	if err != nil {
		return nil, fmt.Errorf("inspect project briefing: %w", err)
	}
	var reasons []string
	if !exists {
		reasons = append(reasons, "MTIX.md is missing")
	} else if !bytes.Equal(actual, expected) {
		reasons = append(reasons, "MTIX.md differs from this binary's generated instructions")
	}
	_, hsErr := os.Lstat(filepath.Join(root, "HYPERSWE.md"))
	if hsErr != nil && !os.IsNotExist(hsErr) {
		return nil, fmt.Errorf("inspect instruction owner: %w", hsErr)
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		pointerReasons, pointerErr := checkProjectPointer(root, name, hsErr != nil)
		if pointerErr != nil {
			return nil, pointerErr
		}
		reasons = append(reasons, pointerReasons...)
	}
	return reasons, nil
}

func checkProjectPointer(root, name string, required bool) ([]string, error) {
	content, _, err := readRegularDoc(filepath.Join(root, name))
	if err != nil {
		return nil, fmt.Errorf("inspect project pointer: %w", err)
	}
	pointer := "@MTIX.md"
	if name == "AGENTS.md" {
		pointer = agentsPointer
	}
	spans, _ := projectPointerSpans(content, pointer, name)
	switch {
	case len(spans) == 0 && required:
		return []string{name + " has no live MTIX.md pointer"}, nil
	case len(spans) > 1:
		return []string{name + " has duplicate live MTIX.md pointers"}, nil
	default:
		return nil, nil
	}
}
