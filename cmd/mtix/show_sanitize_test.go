// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// MTIX-100.1: text that `mtix show` prints is sanitized and normalized, so
// an annotation, description or prompt cannot rewrite what a reader sees.

// showLabelLine matches the label of every top-level line runShow prints.
var showLabelLine = regexp.MustCompile(
	`^(ID|Title|Status|Priority|Type|Assignee|Desc|Annotations|Prompt|Progress|Created):`)

// renderAnnotations runs writeAnnotations into a buffer instead of stdout.
func renderAnnotations(t *testing.T, annotations []model.Annotation) string {
	t.Helper()
	var buf bytes.Buffer
	writeAnnotations(&humanWriter{w: &buf}, annotations)
	return buf.String()
}

// assertSafeLayout checks the properties every rendered block must have:
// no escape or carriage-return bytes, no whitespace-only lines, and no
// content line at column 0 other than a label line.
func assertSafeLayout(t *testing.T, out string) {
	t.Helper()
	assert.NotContains(t, out, "\x1b", "escape bytes must not reach the terminal")
	assert.NotContains(t, out, "\r", "carriage returns must not reach the terminal")
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		assert.NotRegexp(t, `^[ \t]+$`, line, "no whitespace-only lines")
		if !showLabelLine.MatchString(line) {
			assert.Regexp(t, `^\s`, line, "content lines are indented: %q", line)
		}
	}
}

// TestWriteAnnotations_UnsafeOrMessyText_PrintedSafely covers stories 1–4:
// control characters removed (LF and TAB kept), CRLF normalized, trailing
// whitespace trimmed, empty text and zero time stated explicitly.
func TestWriteAnnotations_UnsafeOrMessyText_PrintedSafely(t *testing.T) {
	at := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name     string
		ann      model.Annotation
		contains []string
		absent   []string
	}{
		{
			name:     "escape and carriage return removed from text",
			ann:      model.Annotation{Author: "rev", Text: "FAIL: broken\r  lead: PASS \x1b[8m hidden", CreatedAt: at},
			contains: []string{"FAIL: broken  lead: PASS [8m hidden"},
		},
		{
			name:     "C0, DEL and C1 controls removed",
			ann:      model.Annotation{Author: "rev", Text: "a\x00b\x07c\x7fd\u009be\u0085f", CreatedAt: at},
			contains: []string{"abcdef"},
		},
		{
			name:     "tab kept",
			ann:      model.Annotation{Author: "rev", Text: "a\tb", CreatedAt: at},
			contains: []string{"a\tb"},
		},
		{
			name:     "controls removed from author and addressee",
			ann:      model.Annotation{Author: "rev\x1b[31mer", Addressee: "age\rnt", Text: "ok", CreatedAt: at},
			contains: []string{"rev[31mer → agent: ok"},
		},
		{
			name:     "newline in author cannot start a fake line",
			ann:      model.Annotation{Author: "evil\nAnnotations: none", Text: "ok", CreatedAt: at},
			contains: []string{"evil Annotations: none: ok"},
			absent:   []string{"\nAnnotations: none"},
		},
		{
			name:     "tabs in author and addressee become spaces, outer spaces trimmed",
			ann:      model.Annotation{Author: " \trev\tx ", Addressee: "\tagent ", Text: "ok", CreatedAt: at},
			contains: []string{"] rev x → agent: ok\n"},
		},
		{
			name:     "leading blank lines trimmed",
			ann:      model.Annotation{Author: "rev", Text: "\n \n\t\r\nverdict", CreatedAt: at},
			contains: []string{"rev: verdict\n"},
		},
		{
			name:     "trailing Unicode whitespace trimmed",
			ann:      model.Annotation{Author: "rev", Text: "tail\u00a0\u3000", CreatedAt: at},
			contains: []string{"rev: tail\n"},
		},
		{
			name:     "Unicode-whitespace-only text rendered as (empty)",
			ann:      model.Annotation{Author: "rev", Text: "\u00a0\n\u2003", CreatedAt: at},
			contains: []string{"rev: (empty)\n"},
		},
		{
			name:     "CRLF normalized to LF",
			ann:      model.Annotation{Author: "rev", Text: "line one\r\nline two\r\n", CreatedAt: at},
			contains: []string{"rev: line one\n", "      line two\n"},
		},
		{
			name:     "trailing newlines and whitespace trimmed",
			ann:      model.Annotation{Author: "rev", Text: "verdict \t\n\n\n", CreatedAt: at},
			contains: []string{"rev: verdict\n"},
		},
		{
			name:     "empty text rendered as (empty)",
			ann:      model.Annotation{Author: "rev", Text: "", CreatedAt: at},
			contains: []string{"rev: (empty)\n"},
		},
		{
			name:     "whitespace-only text rendered as (empty)",
			ann:      model.Annotation{Author: "rev", Text: " \n\t \r\n", CreatedAt: at},
			contains: []string{"rev: (empty)\n"},
		},
		{
			name:     "zero created_at rendered as unknown time",
			ann:      model.Annotation{Author: "rev", Text: "ok"},
			contains: []string{"[unknown time] rev: ok\n"},
			absent:   []string{"0001-01-01"},
		},
		{
			name:     "blank line inside text has no trailing indentation",
			ann:      model.Annotation{Author: "rev", Text: "para one\n\npara two", CreatedAt: at},
			contains: []string{"rev: para one\n\n      para two\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderAnnotations(t, []model.Annotation{tt.ann})
			assertSafeLayout(t, out)
			for _, want := range tt.contains {
				assert.Contains(t, out, want)
			}
			for _, bad := range tt.absent {
				assert.NotContains(t, out, bad)
			}
		})
	}
}

// TestRunShow_MultilineDescriptionAndPrompt_ContinuationLinesIndented covers
// story 5: no line of a description or prompt starts at column 0, so a line
// that reads like a label cannot pass for one; control characters are gone.
func TestRunShow_MultilineDescriptionAndPrompt_ContinuationLinesIndented(t *testing.T) {
	initTestApp(t)
	node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
		Project:     "TEST",
		Title:       "Layout target",
		Description: "\n  \nfirst line\nAnnotations: none\r\n\x1b[2Jcleared?",
		Prompt:      "p1\r\nID:       FAKE-1\x1b[31m red\rZ",
		Creator:     "tester",
	})
	require.NoError(t, err)
	captureStdout(t, func() { require.NoError(t, runAnnotate(node.ID, "real verdict")) })

	out := showOutput(t, node.ID)

	assertSafeLayout(t, out)
	assert.NotRegexp(t, `(?m)^Annotations: none$`, out, "a description line must not pass for the marker")
	assert.NotRegexp(t, `(?m)^ID:\s+FAKE-1`, out, "a prompt line must not pass for a label")
	assert.Contains(t, out, "Desc:     first line\n", "leading blank lines are trimmed")
	assert.Contains(t, out, "[2Jcleared?")
	assert.Contains(t, out, "Prompt:   p1\n"+showValueIndent+"ID:       FAKE-1[31m redZ\n")
}

// TestRunShow_LongPrompt_CutOnCharacterBoundaryAfterNormalizing pins the
// prompt cut: 97 characters of normalized text plus "...", counted before
// continuation lines are indented, and never splitting a multi-byte
// character (a split one leaves a stray byte, such as 0x9B, which a
// terminal can read as a control).
func TestRunShow_LongPrompt_CutOnCharacterBoundaryAfterNormalizing(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{
			name:   "cut counted before indenting",
			prompt: "line1\n" + strings.Repeat("y", 200),
			want:   "Prompt:   line1\n" + showValueIndent + strings.Repeat("y", 91) + "...\n",
		},
		{
			name:   "multi-byte character not split",
			prompt: strings.Repeat("x", 95) + strings.Repeat("ᛛ", 10),
			want:   "Prompt:   " + strings.Repeat("x", 95) + "ᛛᛛ...\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
				Project: "TEST", Title: "Cut target", Prompt: tt.prompt, Creator: "tester",
			})
			require.NoError(t, err)

			out := showOutput(t, node.ID)

			assert.True(t, utf8.ValidString(out), "output must be valid UTF-8")
			assert.Contains(t, out, tt.want)
		})
	}
}

// TestRunShow_DescriptionOrPromptWithoutPrintableText_Omitted pins that a
// description or prompt which normalizes to nothing (only whitespace or
// control characters) prints no Desc or Prompt line, as if it were unset;
// --json still has the stored value.
func TestRunShow_DescriptionOrPromptWithoutPrintableText_Omitted(t *testing.T) {
	initTestApp(t)
	node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
		Project:     "TEST",
		Title:       "Blank target",
		Description: "\x1b\x07\r\n \t",
		Prompt:      "\x00 \n\u00a0",
		Creator:     "tester",
	})
	require.NoError(t, err)

	out := showOutput(t, node.ID)

	assertSafeLayout(t, out)
	assert.NotRegexp(t, `(?m)^Desc:`, out)
	assert.NotRegexp(t, `(?m)^Prompt:`, out)
}

// TestRunShow_JSONOutput_KeepsRawNodeRecord covers story 6: --json stays the
// raw node record, control characters and CRLF included (it is data).
func TestRunShow_JSONOutput_KeepsRawNodeRecord(t *testing.T) {
	initTestApp(t)
	rawDesc := "desc\r\nwith \x1b[8m escape"
	node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
		Project: "TEST", Title: "JSON target", Description: rawDesc, Creator: "tester",
	})
	require.NoError(t, err)
	rawText := "FAIL\r\n\x1b[8m hidden \n\n"
	require.NoError(t, app.store.SetAnnotations(context.Background(), node.ID, []model.Annotation{
		{ID: "01K0000000000000000000000J", Author: "rev\x07", Text: rawText},
	}))

	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	out := captureStdout(t, func() { require.NoError(t, runShow(node.ID)) })

	var got model.Node
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, rawDesc, got.Description)
	require.Len(t, got.Annotations, 1)
	assert.Equal(t, rawText, got.Annotations[0].Text)
	assert.Equal(t, "rev\x07", got.Annotations[0].Author)
	assert.True(t, got.Annotations[0].CreatedAt.IsZero())
}

// TestTruncateChars_Limits_CutsOnRunesWithEllipsis pins truncateChars: runes
// are counted, the cut keeps limit-3 of them plus "...", and a limit of 3
// or less leaves the text as is.
func TestTruncateChars_Limits_CutsOnRunesWithEllipsis(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"shorter than limit unchanged", "abc", 5, "abc"},
		{"exactly limit unchanged", "ᛛᛛᛛᛛᛛ", 5, "ᛛᛛᛛᛛᛛ"},
		{"longer cut with ellipsis", "abcdefgh", 5, "ab..."},
		{"multi-byte runes counted as one", "ᛛᛛᛛᛛᛛᛛ", 5, "ᛛᛛ..."},
		{"limit of 3 or less unchanged", "abcdefgh", 3, "abcdefgh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, truncateChars(tt.in, tt.limit))
		})
	}
}
