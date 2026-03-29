// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// output.go tests — StatusIcon, ProgressBar, Truncate, FormatNodeRow, etc.
// ============================================================================

// TestStatusIcon_AllStatuses_ReturnsCorrectIcons verifies all status icons per FR-6.2.
func TestStatusIcon_AllStatuses_ReturnsCorrectIcons(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"done", "✓"},
		{"in_progress", "●"},
		{"open", "○"},
		{"blocked", "⛔"},
		{"deferred", "⏸"},
		{"cancelled", "✕"},
		{"invalidated", "⚠"},
		{"unknown_status", "?"},
		{"", "?"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := StatusIcon(tt.status)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestProgressBar_VariousProgressValues_FormatsCorrectly verifies progress bar rendering.
func TestProgressBar_VariousProgressValues_FormatsCorrectly(t *testing.T) {
	tests := []struct {
		name      string
		progress  float64
		width     int
		wantPct   string
		wantFilled int
		wantEmpty int
	}{
		{"zero", 0.0, 5, "0%", 0, 5},
		{"quarter", 0.25, 4, "25%", 1, 3},
		{"half", 0.5, 10, "50%", 5, 5},
		{"three_quarter", 0.75, 8, "75%", 6, 2},
		{"full", 1.0, 5, "100%", 5, 0},
		{"zero_width", 0.5, 0, "50%", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bar := ProgressBar(tt.progress, tt.width)
			// Verify format: [█░...] XX%
			assert.Contains(t, bar, "[")
			assert.Contains(t, bar, "]")
			assert.Contains(t, bar, tt.wantPct)

			// Count filled and empty blocks.
			filledCount := bytes.Count([]byte(bar), []byte("█"))
			emptyCount := bytes.Count([]byte(bar), []byte("░"))
			assert.Equal(t, tt.wantFilled, filledCount)
			assert.Equal(t, tt.wantEmpty, emptyCount)
		})
	}
}

// TestProgressBar_NegativeProgress_ClampedToZero verifies negative progress handling.
func TestProgressBar_NegativeProgress_ClampedToZero(t *testing.T) {
	bar := ProgressBar(-1.0, 5)
	assert.NotContains(t, bar, "█")
	assert.Contains(t, bar, "░░░░░") // All empty (filled clamped to 0)
	// Percentage displays raw value: -1.0 * 100 = -100%.
	assert.Contains(t, bar, "-100%")
}

// TestProgressBar_ExcessiveProgress_ClampedToFull verifies excessive progress handling.
func TestProgressBar_ExcessiveProgress_ClampedToFull(t *testing.T) {
	bar := ProgressBar(2.5, 5)
	assert.NotContains(t, bar, "░")
	assert.Contains(t, bar, "█████") // All filled
	assert.Contains(t, bar, "250%")
}

// TestProgressBar_LargeWidth_FormatsCorrectly verifies large progress bar.
func TestProgressBar_LargeWidth_FormatsCorrectly(t *testing.T) {
	bar := ProgressBar(0.5, 100)
	filledCount := bytes.Count([]byte(bar), []byte("█"))
	emptyCount := bytes.Count([]byte(bar), []byte("░"))
	assert.Equal(t, 50, filledCount)
	assert.Equal(t, 50, emptyCount)
}

// TestStatusColor_AllStatuses_ReturnsCorrectColors verifies status color codes.
func TestStatusColor_AllStatuses_ReturnsCorrectColors(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"done", colorGreen},
		{"in_progress", colorCyan},
		{"open", ""},
		{"blocked", colorRed},
		{"deferred", colorYellow},
		{"cancelled", colorGray},
		{"invalidated", colorGray},
		{"unknown_status", ""},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := StatusColor(tt.status)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestTruncate_VariousStrings_TruncatesCorrectly verifies string truncation.
func TestTruncate_VariousStrings_TruncatesCorrectly(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short_unchanged", "Hi", 10, "Hi"},
		{"exact_length", "Hello", 5, "Hello"},
		{"exact_plus_one", "Hello", 6, "Hello"},
		{"truncated", "Hello World!", 8, "Hello..."},
		{"truncated_minimal", "Hello World", 6, "Hel..."},
		{"max_len_3_returned_as_is", "Hello", 3, "Hello"},
		{"max_len_2_returned_as_is", "Hello", 2, "Hello"},
		{"max_len_1_returned_as_is", "Hello", 1, "Hello"},
		{"empty_string", "", 10, ""},
		{"very_long_string", "This is a very long string that should definitely be truncated", 20, "This is a very lo..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.input, tt.maxLen)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestColorize_WithColorEnabled_AddsANSICode verifies ANSI coloring when enabled.
func TestColorize_WithColorEnabled_AddsANSICode(t *testing.T) {
	text := "test"
	result := colorize(text, colorGreen, true)
	assert.Contains(t, result, "\033[32m")
	assert.Contains(t, result, "\033[0m")
	assert.Contains(t, result, "test")
}

// TestColorize_WithColorDisabled_NoANSI verifies plain text when disabled.
func TestColorize_WithColorDisabled_NoANSI(t *testing.T) {
	text := "test"
	result := colorize(text, colorGreen, false)
	assert.Equal(t, "test", result)
	assert.NotContains(t, result, "\033")
}

// TestColorize_EmptyColorCode_NoWrap verifies empty color doesn't wrap text.
func TestColorize_EmptyColorCode_NoWrap(t *testing.T) {
	result := colorize("test", "", true)
	assert.Equal(t, "test", result)
	assert.NotContains(t, result, "\033")
}

// TestColorize_EmptyText_ReturnsEmpty verifies empty text handling.
func TestColorize_EmptyText_ReturnsEmpty(t *testing.T) {
	result := colorize("", colorGreen, true)
	assert.Equal(t, colorGreen+colorReset, result)
}

// TestFormatNodeRow_AllFields_IncludedAndFormatted verifies node row formatting.
func TestFormatNodeRow_AllFields_IncludedAndFormatted(t *testing.T) {
	row := FormatNodeRow("PROJ-1", "open", 2, "Test Node", 0.5, false)

	require.Len(t, row, 5, "row should have 5 columns")
	assert.Equal(t, "PROJ-1", row[0])
	assert.Contains(t, row[1], "○")
	assert.Contains(t, row[1], "open")
	assert.Equal(t, "2", row[2])
	assert.Equal(t, "50%", row[3])
	assert.Equal(t, "Test Node", row[4])
}

// TestFormatNodeRow_WithColor_IncludesANSICodes verifies colored row formatting.
func TestFormatNodeRow_WithColor_IncludesANSICodes(t *testing.T) {
	row := FormatNodeRow("PROJ-1", "done", 1, "Title", 1.0, true)

	statusCell := row[1]
	assert.Contains(t, statusCell, "✓")
	assert.Contains(t, statusCell, "done")
	assert.Contains(t, statusCell, "\033[") // ANSI code present
}

// TestFormatNodeRow_LongTitle_TruncatedTo50Chars verifies title truncation in rows.
func TestFormatNodeRow_LongTitle_TruncatedTo50Chars(t *testing.T) {
	longTitle := "This is a very long title that exceeds fifty characters and should be truncated now"
	row := FormatNodeRow("PROJ-1", "open", 3, longTitle, 0.0, false)

	assert.True(t, len(row[4]) <= 50, "title column should be ≤ 50 chars")
}

// TestFormatNodeRow_VariousPriorities_FormattedAsStrings verifies priority formatting.
func TestFormatNodeRow_VariousPriorities_FormattedAsStrings(t *testing.T) {
	tests := []struct {
		priority int
		want     string
	}{
		{1, "1"},
		{5, "5"},
		{0, "0"},
		{99, "99"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			row := FormatNodeRow("PROJ-1", "open", tt.priority, "Title", 0.0, false)
			assert.Equal(t, tt.want, row[2])
		})
	}
}

// TestFormatNodeRow_VariousProgress_FormattedAsPercentage verifies progress formatting.
func TestFormatNodeRow_VariousProgress_FormattedAsPercentage(t *testing.T) {
	tests := []struct {
		progress float64
		want     string
	}{
		{0.0, "0%"},
		{0.25, "25%"},
		{0.5, "50%"},
		{0.75, "75%"},
		{1.0, "100%"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			row := FormatNodeRow("PROJ-1", "open", 1, "Title", tt.progress, false)
			assert.Equal(t, tt.want, row[3])
		})
	}
}

// TestTreeLine_RootNode_NoConnector verifies root node (depth 0) format.
func TestTreeLine_RootNode_NoConnector(t *testing.T) {
	line := TreeLine("PROJ-1", "open", "Root Task", 0.5, "", true, 0, false)

	assert.Contains(t, line, "PROJ-1")
	assert.Contains(t, line, "open")
	assert.Contains(t, line, "Root Task")
	assert.NotContains(t, line, "├")
	assert.NotContains(t, line, "└")
	assert.NotContains(t, line, "│")
}

// TestTreeLine_LastChild_UsesLowerConnector verifies last child uses └── connector.
func TestTreeLine_LastChild_UsesLowerConnector(t *testing.T) {
	line := TreeLine("PROJ-1.2", "done", "Child", 1.0, "", true, 1, false)

	assert.Contains(t, line, "└── ")
	assert.Contains(t, line, "PROJ-1.2")
	assert.Contains(t, line, "done")
	assert.Contains(t, line, "Child")
}

// TestTreeLine_NonLastChild_UsesTeeConnector verifies non-last child uses ├── connector.
func TestTreeLine_NonLastChild_UsesTeeConnector(t *testing.T) {
	line := TreeLine("PROJ-1.1", "in_progress", "Other Child", 0.3, "", false, 1, false)

	assert.Contains(t, line, "├── ")
	assert.Contains(t, line, "PROJ-1.1")
	assert.Contains(t, line, "in_progress")
	assert.Contains(t, line, "Other Child")
}

// TestTreeLine_WithPrefix_IncludesPrefix verifies prefix inclusion in tree line.
func TestTreeLine_WithPrefix_IncludesPrefix(t *testing.T) {
	prefix := "│   "
	line := TreeLine("PROJ-1.1.1", "blocked", "Nested", 0.0, prefix, true, 2, false)

	assert.Contains(t, line, prefix)
	assert.Contains(t, line, "PROJ-1.1.1")
	assert.Contains(t, line, "blocked")
}

// TestTreeLine_WithProgress_IncludesProgressBar verifies progress bar in tree line.
func TestTreeLine_WithProgress_IncludesProgressBar(t *testing.T) {
	line := TreeLine("PROJ-1", "open", "Task", 0.5, "", true, 0, false)

	assert.Contains(t, line, "[")
	assert.Contains(t, line, "]")
	assert.Contains(t, line, "%")
}

// TestTreeLine_ZeroProgress_NoProgressBar verifies zero progress omits progress bar.
func TestTreeLine_ZeroProgress_NoProgressBar(t *testing.T) {
	line := TreeLine("PROJ-1", "open", "Task", 0.0, "", true, 0, false)

	// Status display "[○ open]" is always present; only the progress bar "█░" is omitted when progress is 0.
	assert.NotContains(t, line, "█")
	assert.NotContains(t, line, "░")
}

// TestTreeLine_WithColor_IncludesANSI verifies colored tree line includes ANSI codes.
func TestTreeLine_WithColor_IncludesANSI(t *testing.T) {
	line := TreeLine("PROJ-1", "done", "Task", 0.0, "", true, 0, true)

	assert.Contains(t, line, "\033[")
}

// TestTreeChildPrefix_Depth0_ReturnsEmpty verifies depth 0 always returns empty.
func TestTreeChildPrefix_Depth0_ReturnsEmpty(t *testing.T) {
	tests := []struct {
		isLast bool
	}{
		{true},
		{false},
	}

	for _, tt := range tests {
		result := TreeChildPrefix("", tt.isLast, 0)
		assert.Equal(t, "", result)
	}
}

// TestTreeChildPrefix_LastChild_UsesSpaces verifies last child uses spaces.
func TestTreeChildPrefix_LastChild_UsesSpaces(t *testing.T) {
	result := TreeChildPrefix("", true, 1)
	assert.Equal(t, "    ", result)
}

// TestTreeChildPrefix_NonLastChild_UsesVerticalBar verifies non-last child uses vertical bar.
func TestTreeChildPrefix_NonLastChild_UsesVerticalBar(t *testing.T) {
	result := TreeChildPrefix("", false, 1)
	assert.Equal(t, "│   ", result)
}

// TestTreeChildPrefix_AccumulatesPrefix verifies prefix accumulation for nested levels.
func TestTreeChildPrefix_AccumulatesPrefix(t *testing.T) {
	// Depth 2: non-last child of non-last parent
	result := TreeChildPrefix("│   ", false, 2)
	assert.Equal(t, "│   │   ", result)

	// Depth 2: last child of non-last parent
	result = TreeChildPrefix("│   ", true, 2)
	assert.Equal(t, "│       ", result)

	// Depth 2: non-last child of last parent
	result = TreeChildPrefix("    ", false, 2)
	assert.Equal(t, "    │   ", result)

	// Depth 2: last child of last parent
	result = TreeChildPrefix("    ", true, 2)
	assert.Equal(t, "        ", result)
}

// TestTreeChildPrefix_DeepNesting_MultiLevelAccumulation verifies deep nesting.
func TestTreeChildPrefix_DeepNesting_MultiLevelAccumulation(t *testing.T) {
	prefix := ""
	prefix = TreeChildPrefix(prefix, false, 1) // "│   "
	assert.Equal(t, "│   ", prefix)

	prefix = TreeChildPrefix(prefix, false, 2) // "│   │   "
	assert.Equal(t, "│   │   ", prefix)

	prefix = TreeChildPrefix(prefix, true, 3) // "│   │       "
	assert.Equal(t, "│   │       ", prefix)
}

// TestNewOutputWriter_JSONMode_ReturnsJSONWriter verifies JSON mode selection.
func TestNewOutputWriter_JSONMode_ReturnsJSONWriter(t *testing.T) {
	w := NewOutputWriter(true)
	_, ok := w.(*jsonWriter)
	assert.True(t, ok)
}

// TestNewOutputWriter_HumanMode_ReturnsHumanWriter verifies human mode selection.
func TestNewOutputWriter_HumanMode_ReturnsHumanWriter(t *testing.T) {
	w := NewOutputWriter(false)
	_, ok := w.(*humanWriter)
	assert.True(t, ok)
}

// TestJSONWriter_WriteJSON_ValidJSON verifies JSON output is valid.
func TestJSONWriter_WriteJSON_ValidJSON(t *testing.T) {
	var buf bytes.Buffer
	w := &jsonWriter{w: &buf}

	data := map[string]interface{}{
		"id":     "PROJ-1",
		"status": "open",
		"count":  42,
	}

	err := w.WriteJSON(data)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(buf.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1", result["id"])
	assert.Equal(t, "open", result["status"])
}

// TestJSONWriter_WriteJSON_InvalidValue_ReturnsError verifies error handling.
func TestJSONWriter_WriteJSON_InvalidValue_ReturnsError(t *testing.T) {
	var buf bytes.Buffer
	w := &jsonWriter{w: &buf}

	// Circular reference causes marshaling error
	circular := make(map[string]interface{})
	circular["self"] = circular

	err := w.WriteJSON(circular)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "marshal JSON output")
}

// TestJSONWriter_WriteHuman_Silent verifies human output is silent in JSON mode.
func TestJSONWriter_WriteHuman_Silent(t *testing.T) {
	var buf bytes.Buffer
	w := &jsonWriter{w: &buf}

	w.WriteHuman("This should be silent %s", "test")
	assert.Empty(t, buf.String())
}

// TestJSONWriter_WriteTable_Silent verifies table output is silent in JSON mode.
func TestJSONWriter_WriteTable_Silent(t *testing.T) {
	var buf bytes.Buffer
	w := &jsonWriter{w: &buf}

	w.WriteTable([]string{"A", "B"}, [][]string{{"1", "2"}})
	assert.Empty(t, buf.String())
}

// TestHumanWriter_WriteHuman_OutputsFormatted verifies human-readable output.
func TestHumanWriter_WriteHuman_OutputsFormatted(t *testing.T) {
	var buf bytes.Buffer
	w := &humanWriter{w: &buf}

	w.WriteHuman("Count: %d, Name: %s\n", 42, "test")
	assert.Equal(t, "Count: 42, Name: test\n", buf.String())
}

// TestHumanWriter_WriteJSON_Silent verifies JSON output is silent in human mode.
func TestHumanWriter_WriteJSON_Silent(t *testing.T) {
	var buf bytes.Buffer
	w := &humanWriter{w: &buf}

	err := w.WriteJSON(map[string]string{"key": "value"})
	require.NoError(t, err)
	assert.Empty(t, buf.String())
}

// TestHumanWriter_WriteTable_FormatsAlignedColumns verifies table formatting.
func TestHumanWriter_WriteTable_FormatsAlignedColumns(t *testing.T) {
	var buf bytes.Buffer
	w := &humanWriter{w: &buf}

	headers := []string{"ID", "Status", "Title"}
	rows := [][]string{
		{"PROJ-1", "open", "First"},
		{"PROJ-2.1", "done", "Second"},
	}

	w.WriteTable(headers, rows)

	output := buf.String()
	lines := bytes.Split([]byte(output), []byte("\n"))

	// Verify structure: header, separator, rows
	assert.True(t, len(lines) >= 4, "should have header, separator, and at least 2 rows")
	assert.Contains(t, output, "ID")
	assert.Contains(t, output, "Status")
	assert.Contains(t, output, "Title")
	assert.Contains(t, output, "PROJ-1")
	assert.Contains(t, output, "PROJ-2.1")
	assert.Contains(t, output, "─") // separator
}

// TestHumanWriter_WriteTable_EmptyRows_NoOutput verifies empty table handling.
func TestHumanWriter_WriteTable_EmptyRows_NoOutput(t *testing.T) {
	var buf bytes.Buffer
	w := &humanWriter{w: &buf}

	w.WriteTable([]string{"A", "B"}, nil)
	assert.Empty(t, buf.String())
}

// TestHumanWriter_WriteTable_SingleRow_FormatsCorrectly verifies single-row table.
func TestHumanWriter_WriteTable_SingleRow_FormatsCorrectly(t *testing.T) {
	var buf bytes.Buffer
	w := &humanWriter{w: &buf}

	headers := []string{"Name", "Value"}
	rows := [][]string{{"Key", "123"}}

	w.WriteTable(headers, rows)

	output := buf.String()
	assert.Contains(t, output, "Name")
	assert.Contains(t, output, "Value")
	assert.Contains(t, output, "Key")
	assert.Contains(t, output, "123")
}

// TestHumanWriter_WriteTable_MissingColumns_HandleGracefully verifies graceful handling of missing columns.
func TestHumanWriter_WriteTable_MissingColumns_HandleGracefully(t *testing.T) {
	var buf bytes.Buffer
	w := &humanWriter{w: &buf}

	headers := []string{"A", "B", "C"}
	rows := [][]string{
		{"1", "2"},      // Only 2 columns
		{"3", "4", "5"}, // All 3 columns
	}

	// Should not panic
	w.WriteTable(headers, rows)
	output := buf.String()
	assert.Contains(t, output, "A")
	assert.Contains(t, output, "B")
	assert.Contains(t, output, "C")
}

// TestHumanWriter_WriteTable_ExtraColumns_Ignored verifies extra columns are ignored.
func TestHumanWriter_WriteTable_ExtraColumns_Ignored(t *testing.T) {
	var buf bytes.Buffer
	w := &humanWriter{w: &buf}

	headers := []string{"A", "B"}
	rows := [][]string{
		{"1", "2", "extra1", "extra2"},
	}

	w.WriteTable(headers, rows)
	output := buf.String()
	assert.Contains(t, output, "1")
	assert.Contains(t, output, "2")
	assert.NotContains(t, output, "extra1")
}

// TestIsTerminal_NilFile_ReturnsFalse verifies nil file handling.
func TestIsTerminal_NilFile_ReturnsFalse(t *testing.T) {
	assert.False(t, isTerminal(nil))
}

// TestIsTerminal_RegularFile_ReturnsFalse verifies regular file detection.
func TestIsTerminal_RegularFile_ReturnsFalse(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "test-*.txt")
	require.NoError(t, err)
	defer func() { _ = tmpFile.Close() }()

	assert.False(t, isTerminal(tmpFile))
}

// ============================================================================
// init.go tests — prefix validation and helpers
// ============================================================================

// TestPrefixRegex_ValidPrefixes_Matches verifies valid prefix matching.
func TestPrefixRegex_ValidPrefixes_Matches(t *testing.T) {
	tests := []string{
		"PROJ",
		"A",
		"PROJECT",
		"PROJ-123",
		"AB-CD-EF",
		"A" + "BCDEFGHIJKLMNOPQRS", // 20 chars total
	}

	for _, prefix := range tests {
		t.Run(prefix, func(t *testing.T) {
			assert.True(t, prefixRegex.MatchString(prefix),
				"prefix %q should match pattern", prefix)
		})
	}
}

// TestPrefixRegex_InvalidPrefixes_DoesNotMatch verifies invalid prefix rejection.
func TestPrefixRegex_InvalidPrefixes_DoesNotMatch(t *testing.T) {
	tests := []struct {
		prefix string
		reason string
	}{
		{"proj", "lowercase"},
		{"123", "starts with digit"},
		{"-PROJ", "starts with hyphen"},
		{"A" + "BCDEFGHIJKLMNOPQRSTU", "exceeds 20 chars (21 total)"},
		{"PROJ_NAME", "underscore not allowed"},
		{"PROJ NAME", "space not allowed"},
		{"", "empty string"},
		{"PROJ@1", "special char"},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			assert.False(t, prefixRegex.MatchString(tt.prefix),
				"prefix %q should not match (reason: %s)", tt.prefix, tt.reason)
		})
	}
}

// TestSplitLines_SingleLine_ReturnsSingleElement verifies single-line parsing.
func TestSplitLines_SingleLine_ReturnsSingleElement(t *testing.T) {
	result := splitLines("hello")
	assert.Equal(t, []string{"hello"}, result)
}

// TestSplitLines_MultipleLines_ReturnsAllLines verifies multi-line parsing.
func TestSplitLines_MultipleLines_ReturnsAllLines(t *testing.T) {
	result := splitLines("line1\nline2\nline3")
	assert.Equal(t, []string{"line1", "line2", "line3"}, result)
}

// TestSplitLines_TrailingNewline_HandledCorrectly verifies trailing newline handling.
func TestSplitLines_TrailingNewline_HandledCorrectly(t *testing.T) {
	result := splitLines("line1\nline2\n")
	// splitLines strips trailing empty segment when string ends with newline.
	assert.Equal(t, []string{"line1", "line2"}, result)
}

// TestSplitLines_EmptyString_ReturnsEmpty verifies empty string handling.
func TestSplitLines_EmptyString_ReturnsEmpty(t *testing.T) {
	result := splitLines("")
	assert.Nil(t, result)
}

// TestSplitLines_OnlyNewlines_ReturnEmptyStrings verifies newline-only handling.
func TestSplitLines_OnlyNewlines_ReturnEmptyStrings(t *testing.T) {
	result := splitLines("\n\n\n")
	// Three newlines produce three segments (empty before each newline); trailing empty is stripped.
	assert.Equal(t, []string{"", "", ""}, result)
}

// TestContains_ExactMatch_ReturnsTrue verifies exact line match.
func TestContains_ExactMatch_ReturnsTrue(t *testing.T) {
	content := "line1\ndocs/\nline3"
	assert.True(t, contains(content, "docs/"))
}

// TestContains_NoMatch_ReturnsFalse verifies non-matching line.
func TestContains_NoMatch_ReturnsFalse(t *testing.T) {
	content := "line1\nline2\nline3"
	assert.False(t, contains(content, "docs/"))
}

// TestContains_PartialMatch_ReturnsFalse verifies partial match is not accepted.
func TestContains_PartialMatch_ReturnsFalse(t *testing.T) {
	content := "build/\nfeat/\n"
	assert.False(t, contains(content, "docs"))
}

// TestContains_EmptyContent_ReturnsFalse verifies empty content handling.
func TestContains_EmptyContent_ReturnsFalse(t *testing.T) {
	assert.False(t, contains("", "docs/"))
}

// TestContains_EmptyEntry_ReturnsFalse verifies empty entry handling.
func TestContains_EmptyEntry_ReturnsFalse(t *testing.T) {
	content := "line1\nline2\n"
	assert.False(t, contains(content, ""))
}

// TestContains_CaseSensitive_ReturnsExpected verifies case sensitivity.
func TestContains_CaseSensitive_ReturnsExpected(t *testing.T) {
	content := "Docs/\ndocs/\n"
	assert.True(t, contains(content, "Docs/"))
	assert.True(t, contains(content, "docs/"))
	assert.False(t, contains(content, "DOCS/"))
}

// ============================================================================
// update.go tests — splitAndTrim helper
// ============================================================================

// TestSplitAndTrim_SingleLabel_ReturnsSingleElement verifies single label parsing.
func TestSplitAndTrim_SingleLabel_ReturnsSingleElement(t *testing.T) {
	result := splitAndTrim("bug")
	assert.Equal(t, []string{"bug"}, result)
}

// TestSplitAndTrim_MultipleLabels_ReturnsAllLabels verifies multiple label parsing.
func TestSplitAndTrim_MultipleLabels_ReturnsAllLabels(t *testing.T) {
	result := splitAndTrim("bug,feature,enhancement")
	assert.Equal(t, []string{"bug", "feature", "enhancement"}, result)
}

// TestSplitAndTrim_WithWhitespace_TrimsCorrectly verifies whitespace trimming.
func TestSplitAndTrim_WithWhitespace_TrimsCorrectly(t *testing.T) {
	result := splitAndTrim("bug , feature , enhancement")
	assert.Equal(t, []string{"bug", "feature", "enhancement"}, result)
}

// TestSplitAndTrim_MixedWhitespace_TrimsAll verifies various whitespace types.
func TestSplitAndTrim_MixedWhitespace_TrimsAll(t *testing.T) {
	result := splitAndTrim(" bug , \t feature \t, \n enhancement ")
	assert.Equal(t, []string{"bug", "feature", "enhancement"}, result)
}

// TestSplitAndTrim_EmptyString_ReturnsEmptySlice verifies empty string handling.
func TestSplitAndTrim_EmptyString_ReturnsEmptySlice(t *testing.T) {
	result := splitAndTrim("")
	assert.Equal(t, []string{""}, result)
}

// TestSplitAndTrim_OnlyCommas_ReturnsEmptyStrings verifies comma-only handling.
func TestSplitAndTrim_OnlyCommas_ReturnsEmptyStrings(t *testing.T) {
	result := splitAndTrim(",,")
	assert.Equal(t, []string{"", "", ""}, result)
}

// TestSplitAndTrim_CommasWithWhitespace_TrimsEmptyLabels verifies trimming of empty labels.
func TestSplitAndTrim_CommasWithWhitespace_TrimsEmptyLabels(t *testing.T) {
	result := splitAndTrim(" , , bug , ")
	assert.Equal(t, []string{"", "", "bug", ""}, result)
}

// TestSplitAndTrim_NoCommas_ReturnsOriginal verifies no-comma case.
func TestSplitAndTrim_NoCommas_ReturnsOriginal(t *testing.T) {
	result := splitAndTrim("single_label")
	assert.Equal(t, []string{"single_label"}, result)
}

// TestSplitAndTrim_SpecialCharacters_PreservedInLabels verifies special chars preserved.
func TestSplitAndTrim_SpecialCharacters_PreservedInLabels(t *testing.T) {
	result := splitAndTrim("bug-fix, v1.0, type:feature")
	assert.Equal(t, []string{"bug-fix", "v1.0", "type:feature"}, result)
}

// ============================================================================
// routing.go tests — exempt command logic
// ============================================================================

// TestIsExemptCommand_AllExemptCommands_ReturnsTrue verifies all exempt commands.
func TestIsExemptCommand_AllExemptCommands_ReturnsTrue(t *testing.T) {
	tests := []string{
		"config", "init", "migrate", "docs", "version", "help",
	}

	for _, cmdName := range tests {
		t.Run(cmdName, func(t *testing.T) {
			cmd := &cobra.Command{Use: cmdName}
			assert.True(t, isExemptCommand(cmd))
		})
	}
}

// TestIsExemptCommand_NonExemptCommands_ReturnsFalse verifies non-exempt commands.
func TestIsExemptCommand_NonExemptCommands_ReturnsFalse(t *testing.T) {
	tests := []string{
		"create", "show", "list", "update", "delete", "claim", "search",
	}

	for _, cmdName := range tests {
		t.Run(cmdName, func(t *testing.T) {
			cmd := &cobra.Command{Use: cmdName}
			assert.False(t, isExemptCommand(cmd))
		})
	}
}

// TestIsExemptCommand_ConfigSubcommand_ReturnsTrue verifies config subcommand exemption.
func TestIsExemptCommand_ConfigSubcommand_ReturnsTrue(t *testing.T) {
	parent := &cobra.Command{Use: "config"}
	child := &cobra.Command{Use: "get"}
	parent.AddCommand(child)

	assert.True(t, isExemptCommand(child))
}

// TestIsExemptCommand_NonExemptSubcommand_ReturnsFalse verifies non-exempt subcommand.
func TestIsExemptCommand_NonExemptSubcommand_ReturnsFalse(t *testing.T) {
	parent := &cobra.Command{Use: "show"}
	child := &cobra.Command{Use: "details"}
	parent.AddCommand(child)

	assert.False(t, isExemptCommand(child))
}

// TestAdminRoutes_AllCommandsPresent_ValidMethods verifies all admin commands are configured.
func TestAdminRoutes_AllCommandsPresent_ValidMethods(t *testing.T) {
	expectedCommands := []string{"backup", "export", "import", "gc", "verify"}

	for _, cmd := range expectedCommands {
		t.Run(cmd, func(t *testing.T) {
			route, ok := adminRoutes[cmd]
			assert.True(t, ok, "admin route for %s should exist", cmd)
			assert.NotEmpty(t, route.method)
			assert.NotEmpty(t, route.path)
		})
	}
}

// TestAdminRoutes_CorrectHTTPMethods_Configured verifies correct HTTP methods.
func TestAdminRoutes_CorrectHTTPMethods_Configured(t *testing.T) {
	tests := []struct {
		cmd    string
		method string
	}{
		{"backup", "POST"},
		{"export", "GET"},
		{"import", "POST"},
		{"gc", "POST"},
		{"verify", "GET"},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			route := adminRoutes[tt.cmd]
			assert.Equal(t, tt.method, route.method)
		})
	}
}

// TestAdminRoutes_CorrectPaths_Configured verifies correct endpoint paths.
func TestAdminRoutes_CorrectPaths_Configured(t *testing.T) {
	tests := []struct {
		cmd  string
		path string
	}{
		{"backup", "/admin/backup"},
		{"export", "/admin/export"},
		{"import", "/admin/import"},
		{"gc", "/admin/gc"},
		{"verify", "/admin/verify"},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			route := adminRoutes[tt.cmd]
			assert.Equal(t, tt.path, route.path)
		})
	}
}

// ============================================================================
// root.go tests — command creation and structure
// ============================================================================

// TestNewRootCmd_CreateSucceeds_ReturnsValidCommand verifies root command creation.
func TestNewRootCmd_CreateSucceeds_ReturnsValidCommand(t *testing.T) {
	rootCmd := newRootCmd()
	require.NotNil(t, rootCmd)
	assert.Equal(t, "mtix", rootCmd.Use)
	assert.NotEmpty(t, rootCmd.Short)
	assert.NotEmpty(t, rootCmd.Long)
}

// TestNewRootCmd_HasPersistentFlags_JSONAndLogLevel verifies persistent flags.
func TestNewRootCmd_HasPersistentFlags_JSONAndLogLevel(t *testing.T) {
	rootCmd := newRootCmd()

	jsonFlag := rootCmd.PersistentFlags().Lookup("json")
	require.NotNil(t, jsonFlag)
	assert.Equal(t, "json", jsonFlag.Name)

	logLevelFlag := rootCmd.PersistentFlags().Lookup("log-level")
	require.NotNil(t, logLevelFlag)
	assert.Equal(t, "log-level", logLevelFlag.Name)
}

// TestNewRootCmd_HasSubcommands_RegisteredCorrectly verifies subcommands registration.
func TestNewRootCmd_HasSubcommands_RegisteredCorrectly(t *testing.T) {
	rootCmd := newRootCmd()

	requiredCommands := []string{
		"init", "config", "create", "show", "list", "tree",
		"update", "claim", "unclaim", "done", "defer", "cancel",
		"delete", "undelete", "comment", "dep", "search",
	}

	for _, cmdName := range requiredCommands {
		t.Run(cmdName, func(t *testing.T) {
			cmd, _, _ := rootCmd.Find([]string{cmdName})
			require.NotNil(t, cmd, "subcommand %q should be registered", cmdName)
		})
	}
}

// TestNewRootCmd_Version_FormattedCorrectly verifies version string format.
func TestNewRootCmd_Version_FormattedCorrectly(t *testing.T) {
	rootCmd := newRootCmd()

	assert.NotEmpty(t, rootCmd.Version)
	assert.Contains(t, rootCmd.Version, "commit:")
	assert.Contains(t, rootCmd.Version, "built:")
}

// TestFindMtixDir_NoMtixDir_ReturnsError verifies error when .mtix not found.
func TestFindMtixDir_NoMtixDir_ReturnsError(t *testing.T) {
	// Change to a temporary directory with no .mtix
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()

	err = os.Chdir(tmpDir)
	require.NoError(t, err)

	_, err = findMtixDir()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), ".mtix directory not found")
}

// TestFindMtixDir_MtixDirExists_ReturnsPath verifies finding .mtix directory.
func TestFindMtixDir_MtixDirExists_ReturnsPath(t *testing.T) {
	tmpDir := t.TempDir()
	mtixDir := filepath.Join(tmpDir, ".mtix")
	err := os.Mkdir(mtixDir, 0o755)
	require.NoError(t, err)

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()

	err = os.Chdir(tmpDir)
	require.NoError(t, err)

	found, err := findMtixDir()
	require.NoError(t, err)
	// Resolve symlinks for macOS compatibility (e.g., /var -> /private/var)
	expectedPath, err := filepath.EvalSymlinks(mtixDir)
	require.NoError(t, err)
	assert.Equal(t, expectedPath, found)
}

// TestFindMtixDir_MtixDirInParent_ReturnsPath verifies finding .mtix in parent directory.
func TestFindMtixDir_MtixDirInParent_ReturnsPath(t *testing.T) {
	tmpDir := t.TempDir()
	mtixDir := filepath.Join(tmpDir, ".mtix")
	err := os.Mkdir(mtixDir, 0o755)
	require.NoError(t, err)

	subDir := filepath.Join(tmpDir, "subdir")
	err = os.Mkdir(subDir, 0o755)
	require.NoError(t, err)

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()

	err = os.Chdir(subDir)
	require.NoError(t, err)

	found, err := findMtixDir()
	require.NoError(t, err)
	// Resolve symlinks for macOS compatibility (e.g., /var -> /private/var)
	expectedPath, err := filepath.EvalSymlinks(mtixDir)
	require.NoError(t, err)
	assert.Equal(t, expectedPath, found)
}

// TestIsRouted_ContextWithRoutedMarker_ReturnsTrue verifies routed context detection.
func TestIsRouted_ContextWithRoutedMarker_ReturnsTrue(t *testing.T) {
	ctx := withRouted(context.Background())
	assert.True(t, isRouted(ctx))
}

// TestIsRouted_ContextWithoutMarker_ReturnsFalse verifies non-routed context.
func TestIsRouted_ContextWithoutMarker_ReturnsFalse(t *testing.T) {
	ctx := context.Background()
	assert.False(t, isRouted(ctx))
}
