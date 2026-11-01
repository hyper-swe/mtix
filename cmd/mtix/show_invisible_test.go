// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestDisplayText_InvisibleFormatChars_RemovedOrSplit covers MTIX-100.7:
// bidirectional and zero-width characters are removed, U+2028 and U+2029
// become line breaks, and text made only of them normalizes to "".
func TestDisplayText_InvisibleFormatChars_RemovedOrSplit(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"bidi override", "FAIL \u202eSSAP\u202c ok", "FAIL SSAP ok"},
		{"bidi isolate", "a\u2067b\u2069c", "abc"},
		{"zero width", "a\u200bb\u200cc\u200dd\u2060e\ufeff", "abcde"},
		{"line separator splits", "a\u2028b", "a\nb"},
		{"paragraph separator splits", "a\u2029b", "a\nb"},
		{"leading separators trimmed as blank lines", "\u2028\u2029text", "text"},
		{"only invisible", "\u202e\u200b\u2066\ufeff", ""},
		{"only invisible and separators", "\u200b\u2028\u2029\u202e", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, displayText(tt.in))
		})
	}
}

// TestSingleLine_LineSeparators_BecomeSpace verifies a separator cannot
// start a new line in a one-line field.
func TestSingleLine_LineSeparators_BecomeSpace(t *testing.T) {
	assert.Equal(t, "a Annotations: none", singleLine("a\u2028Annotations: none"))
	assert.Equal(t, "ab", singleLine("\u202ea\u200bb"))
}

// TestWriteAnnotations_InvisibleOnlyText_RenderedAsEmpty pins acceptance 3
// for annotations.
func TestWriteAnnotations_InvisibleOnlyText_RenderedAsEmpty(t *testing.T) {
	at := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	out := renderAnnotations(t, []model.Annotation{
		{Author: "rev", Text: "\u202e\u200b\u2028", CreatedAt: at},
	})
	assert.Contains(t, out, "rev: (empty)\n")
}

// TestRunShow_InvisibleOnlyDescriptionOrPrompt_OmittedJSONRaw pins
// acceptance 3 for Desc and Prompt, and acceptance 5 for --json.
func TestRunShow_InvisibleOnlyDescriptionOrPrompt_OmittedJSONRaw(t *testing.T) {
	initTestApp(t)
	raw := "\u202e\u200b\u2028"
	node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
		Project:     "TEST",
		Title:       "Invisible target",
		Description: raw,
		Prompt:      raw,
		Creator:     "tester",
	})
	require.NoError(t, err)

	out := showOutput(t, node.ID)

	assert.NotRegexp(t, `(?m)^Desc:`, out)
	assert.NotRegexp(t, `(?m)^Prompt:`, out)
	assert.NotContains(t, out, "\u202e")

	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	jsonOut := captureStdout(t, func() { require.NoError(t, runShow(node.ID)) })
	var got model.Node
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &got))
	assert.Equal(t, raw, got.Description)
	assert.Equal(t, raw, got.Prompt)
}
