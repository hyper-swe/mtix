// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strings"
)

// estimateTokens estimates token count using chars/4 per FR-12.4a.
// Configurable via context.token_estimator config key.
// 10-15% estimation error margin is acceptable.
func estimateTokens(text string) int {
	return len(text) / 4
}

// truncateAssembledPrompt applies token-budget truncation per FR-12.4.
// Truncation priority:
//  1. Target node always included in full
//  2. Immediate parent's prompt always included
//  3. Ancestor titles included as one-liners
//  4. Distant ancestor prompts truncated to first sentence, then dropped entirely
//
// Returns the truncated assembled prompt string.
func truncateAssembledPrompt(chain []ContextEntry, maxTokens int) string {
	if maxTokens <= 0 {
		return "" // Should not be called with zero budget.
	}

	// Phase 1: Build target and parent sections (always preserved).
	targetIdx := len(chain) - 1
	parentIdx := targetIdx - 1

	targetSection := buildEntrySection(chain[targetIdx])
	var parentSection string
	if parentIdx >= 0 {
		parentSection = buildEntrySection(chain[parentIdx])
	}

	// Check if target + parent fit within budget.
	preservedTokens := estimateTokens(targetSection) + estimateTokens(parentSection)

	// Phase 2: Build ancestor sections with progressive truncation.
	ancestorSections := make([]string, 0, len(chain))
	omittedCount := 0

	for i := 0; i < len(chain); i++ {
		if i == targetIdx || i == parentIdx {
			continue
		}

		entry := chain[i]
		fullSection := buildEntrySection(entry)
		fullTokens := estimateTokens(fullSection)

		// Try full section first.
		if preservedTokens+fullTokens <= maxTokens {
			ancestorSections = append(ancestorSections, fullSection)
			preservedTokens += fullTokens
			continue
		}

		// Try title-only one-liner.
		titleOnly := buildTitleOnlySection(entry)
		titleTokens := estimateTokens(titleOnly)

		if preservedTokens+titleTokens <= maxTokens {
			ancestorSections = append(ancestorSections, titleOnly)
			preservedTokens += titleTokens
			continue
		}

		// Drop entirely.
		omittedCount++
	}

	// Phase 3: Assemble final prompt.
	var sb strings.Builder

	// Write ancestor sections first (root-first order).
	for i, section := range ancestorSections {
		if i > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString(section)
	}

	// Write parent section.
	if parentSection != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString(parentSection)
	}

	// Write target section.
	if sb.Len() > 0 {
		sb.WriteString("\n\n---\n\n")
	}
	sb.WriteString(targetSection)

	// Append truncation note if content was dropped.
	if omittedCount > 0 {
		fmt.Fprintf(&sb,
			"\n\n[TRUNCATED: %d ancestor prompts omitted due to token budget]",
			omittedCount,
		)
	}

	return sb.String()
}

// buildEntrySection builds a full section for a context entry.
func buildEntrySection(entry ContextEntry) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## %s %s: %s\n",
		strings.ToUpper(string(entry.Tier)), entry.ID, entry.Title)

	if entry.Prompt != "" {
		attribution := classifyCreator(entry.Creator)
		fmt.Fprintf(&sb, "\n%s\n%s\n", attribution, entry.Prompt)
	}

	for _, a := range entry.Annotations {
		fmt.Fprintf(&sb, "\n[ANNOTATION by %s]\n%s\n", a.Author, a.Text)
	}

	return sb.String()
}

// buildTitleOnlySection builds a minimal one-liner for a context entry.
func buildTitleOnlySection(entry ContextEntry) string {
	return fmt.Sprintf("## %s %s: %s\n",
		strings.ToUpper(string(entry.Tier)), entry.ID, entry.Title)
}
