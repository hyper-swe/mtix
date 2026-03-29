// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// isTerminal returns true if the given file descriptor is a terminal.
// Uses os.File.Stat() mode to detect TTY without external dependencies per FR-6.2.
// Falls back to checking the TERM environment variable.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	// Character devices (like terminals) have the ModeCharDevice bit set.
	return stat.Mode()&os.ModeCharDevice != 0
}

// ANSI color codes for terminal output.
const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorCyan   = "\033[36m"
	colorGray = "\033[90m"
)

// OutputWriter abstracts CLI output between JSON and human-readable formats per FR-6.2.
// Commands use this interface instead of fmt.Printf/json.Marshal directly.
type OutputWriter interface {
	// WriteJSON outputs a value as formatted JSON (for --json mode).
	WriteJSON(v any) error

	// WriteHuman outputs human-readable text (ignored in --json mode).
	WriteHuman(format string, args ...any)

	// WriteTable outputs tabular data with aligned columns.
	WriteTable(headers []string, rows [][]string)
}

// humanWriter outputs human-readable formatted text.
type humanWriter struct {
	w     io.Writer
	color bool // true if terminal supports color
}

// jsonWriter outputs machine-readable JSON.
type jsonWriter struct {
	w io.Writer
}

// NewOutputWriter creates the appropriate writer based on --json flag.
// Color output is enabled only when stdout is a TTY per FR-6.2.
func NewOutputWriter(jsonMode bool) OutputWriter {
	if jsonMode {
		return &jsonWriter{w: os.Stdout}
	}
	return &humanWriter{
		w:     os.Stdout,
		color: isTerminal(os.Stdout),
	}
}

// --- Human writer ---

func (h *humanWriter) WriteJSON(_ any) error {
	// In human mode, JSON output is suppressed — callers should use WriteHuman.
	return nil
}

func (h *humanWriter) WriteHuman(format string, args ...any) {
	fmt.Fprintf(h.w, format, args...)
}

func (h *humanWriter) WriteTable(headers []string, rows [][]string) {
	if len(rows) == 0 {
		return
	}

	widths := calcColumnWidths(headers, rows)
	h.writeTableRow(headers, widths)
	h.writeTableSeparator(widths)
	for _, row := range rows {
		h.writeTableRow(row, widths)
	}
}

// calcColumnWidths computes the maximum width for each column.
func calcColumnWidths(headers []string, rows [][]string) []int {
	widths := make([]int, len(headers))
	for i, hdr := range headers {
		widths[i] = len(hdr)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	return widths
}

// writeTableRow prints a single row with proper column alignment.
func (h *humanWriter) writeTableRow(cells []string, widths []int) {
	for i, cell := range cells {
		if i >= len(widths) {
			break
		}
		if i > 0 {
			fmt.Fprint(h.w, "  ")
		}
		fmt.Fprintf(h.w, "%-*s", widths[i], cell)
	}
	fmt.Fprintln(h.w)
}

// writeTableSeparator prints the separator line under headers.
func (h *humanWriter) writeTableSeparator(widths []int) {
	for i, w := range widths {
		if i > 0 {
			fmt.Fprint(h.w, "  ")
		}
		fmt.Fprint(h.w, strings.Repeat("─", w))
	}
	fmt.Fprintln(h.w)
}

// --- JSON writer ---

func (j *jsonWriter) WriteJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON output: %w", err)
	}
	fmt.Fprintln(j.w, string(data))
	return nil
}

func (j *jsonWriter) WriteHuman(_ string, _ ...any) {
	// In JSON mode, human-readable output is suppressed.
}

func (j *jsonWriter) WriteTable(_ []string, _ [][]string) {
	// In JSON mode, table output is suppressed — callers should use WriteJSON.
}

// StatusIcon returns a Unicode icon for a node status per FR-6.2.
func StatusIcon(status string) string {
	switch status {
	case "done":
		return "✓"
	case "in_progress":
		return "●"
	case "open":
		return "○"
	case "blocked":
		return "⛔"
	case "deferred":
		return "⏸"
	case "cancelled":
		return "✕"
	case "invalidated":
		return "⚠"
	default:
		return "?"
	}
}

// ProgressBar returns a Unicode progress bar string.
func ProgressBar(progress float64, width int) string {
	filled := int(progress * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	empty := width - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	return fmt.Sprintf("[%s] %.0f%%", bar, progress*100)
}

// StatusColor returns the ANSI color code for a status.
func StatusColor(status string) string {
	switch status {
	case "done":
		return colorGreen
	case "in_progress":
		return colorCyan
	case "open":
		return ""
	case "blocked":
		return colorRed
	case "deferred":
		return colorYellow
	case "cancelled", "invalidated":
		return colorGray
	default:
		return ""
	}
}

// colorize wraps text in ANSI color codes if color is enabled.
func colorize(text, color string, enabled bool) string {
	if !enabled || color == "" {
		return text
	}
	return color + text + colorReset
}

// Truncate shortens a string to maxLen characters, appending "..." if truncated.
func Truncate(s string, maxLen int) string {
	if maxLen <= 3 {
		return s
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// FormatNodeRow returns a formatted table row for a node.
func FormatNodeRow(id, status string, priority int, title string, progress float64, useColor bool) []string {
	icon := StatusIcon(status)
	statusStr := fmt.Sprintf("%s %s", icon, status)
	if useColor {
		clr := StatusColor(status)
		statusStr = colorize(statusStr, clr, true)
	}
	progressStr := fmt.Sprintf("%.0f%%", progress*100)
	return []string{id, statusStr, fmt.Sprintf("%d", priority), progressStr, Truncate(title, 50)}
}

// TreeLine renders a single line of an ASCII tree.
func TreeLine(id, status, title string, progress float64, prefix string, isLast bool, depth int, useColor bool) string {
	icon := StatusIcon(status)
	statusStr := fmt.Sprintf("%s %s", icon, status)
	if useColor {
		clr := StatusColor(status)
		statusStr = colorize(statusStr, clr, true)
	}

	progressStr := ""
	if progress > 0 {
		progressStr = fmt.Sprintf(" %s", ProgressBar(progress, 8))
	}

	if depth == 0 {
		return fmt.Sprintf("%s [%s]%s %s", id, statusStr, progressStr, title)
	}

	connector := "├── "
	if isLast {
		connector = "└── "
	}
	return fmt.Sprintf("%s%s%s [%s]%s %s", prefix, connector, id, statusStr, progressStr, title)
}

// TreeChildPrefix returns the prefix for a child node in an ASCII tree.
func TreeChildPrefix(parentPrefix string, isLast bool, depth int) string {
	if depth == 0 {
		return ""
	}
	if isLast {
		return parentPrefix + "    "
	}
	return parentPrefix + "│   "
}
