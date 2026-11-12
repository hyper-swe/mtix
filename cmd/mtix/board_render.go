// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/service"
)

// consoleIsUTF8 reports whether the console can show UTF-8 (MTIX-255.1). It
// reads the first non-empty of LC_ALL, LC_CTYPE and LANG; with none set it is
// false (plain ASCII is the safe default), except in Windows Terminal.
func consoleIsUTF8() bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := os.Getenv(k); v != "" {
			v = strings.ToLower(v)
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return runtime.GOOS == "windows" && os.Getenv("WT_SESSION") != ""
}

// boardMarker returns the status marker for a column: the Unicode icon on a
// UTF-8 console, otherwise a fixed ASCII marker (MTIX-255.1).
func boardMarker(status string, utf8Console bool) string {
	if utf8Console {
		return StatusIcon(status)
	}
	switch status {
	case "open":
		return "[ ]"
	case "in_progress":
		return "[~]"
	case "blocked":
		return "[!]"
	case "deferred":
		return "[-]"
	case "done":
		return "[x]"
	case "cancelled":
		return "[/]"
	default:
		return "[?]"
	}
}

// renderBoardTable prints one section per column: a "name (count)" header,
// one line per item, and a "... N more" line when items were capped.
func renderBoardTable(board *service.Board, utf8Console bool) error {
	var sb strings.Builder
	for i, col := range board.Columns {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "%s (%d)\n", col.Name, col.Count)
		marker := boardMarker(col.Name, utf8Console)
		for _, it := range col.Items {
			line := fmt.Sprintf("  %s %s  P%d  %s", marker, it.ID, it.Priority, Truncate(format.SafeLine(it.Title), 60))
			if it.Assignee != "" {
				line += "  @" + format.SafeLine(it.Assignee)
			}
			sb.WriteString(line + "\n")
		}
		if more := col.Count - len(col.Items); more > 0 {
			fmt.Fprintf(&sb, "  ... %d more\n", more)
		}
	}
	text := sb.String()
	if !utf8Console {
		text = asciiOnly(text)
	}
	_, err := fmt.Fprint(os.Stdout, text)
	return err
}

// asciiOnly replaces every non-ASCII rune with '?' so a non-UTF-8 console
// never receives bytes it cannot show.
func asciiOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r > 127 {
			return '?'
		}
		return r
	}, s)
}
