// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/testutil"
)

const (
	annotationIDOne = "01K0000000000000000000000A"
	annotationIDTwo = "01K0000000000000000000000B"
)

func seedShowAnnotations(t *testing.T, anns ...model.Annotation) string {
	t.Helper()
	initTestApp(t)
	id := createShowNode(t, "A description")
	require.NoError(t, app.store.SetAnnotations(context.Background(), id, anns))
	return id
}

// TestRunShow_Annotations_PrintShortIDThatResolveAnnotationAccepts closes the
// loop the ticket asks for: the id show prints is accepted by resolve-annotation.
func TestRunShow_Annotations_PrintShortIDThatResolveAnnotationAccepts(t *testing.T) {
	at := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	id := seedShowAnnotations(t, model.Annotation{ID: annotationIDOne, Author: "reviewer-a", Text: "PASS", CreatedAt: at})
	out := showOutput(t, id)
	short := model.ShortAnnotationID(annotationIDOne)
	assert.Contains(t, out, `[2026-09-01T09:30:00Z] (id "`+short+`") reviewer-a: PASS`)
	assert.NotContains(t, out, annotationIDOne, "show prints the short form only")

	app.jsonOutput = false
	printed := captureStdout(t, func() { require.NoError(t, runResolveAnnotation(id, short)) })
	assert.Contains(t, printed, `"`+annotationIDOne+`"`, "the full id is reported")
	got, err := app.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, got.Annotations[0].Resolved)
}

func TestRunShow_JSON_KeepsFullAnnotationID(t *testing.T) {
	at := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	id := seedShowAnnotations(t, model.Annotation{ID: annotationIDOne, Author: "a", Text: "t", CreatedAt: at})
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	out := captureStdout(t, func() { require.NoError(t, runShow(id)) })
	var node struct {
		Annotations []model.Annotation `json:"annotations"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &node))
	require.Len(t, node.Annotations, 1)
	assert.Equal(t, annotationIDOne, node.Annotations[0].ID)
}

func TestRunShow_HostileAnnotationID_RendersPrintableASCII(t *testing.T) {
	at := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	for _, hostile := range testutil.HostileIDs() {
		t.Run(hostile.Name, func(t *testing.T) {
			id := seedShowAnnotations(t, model.Annotation{ID: hostile.Value, Author: "a", Text: "t", CreatedAt: at})
			out := showOutput(t, id)
			line := ""
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, "(id ") {
					line = l
				}
			}
			require.NotEmpty(t, line, "the annotation line must show an id:\n%s", out)
			for _, r := range line {
				assert.True(t, r >= 0x20 && r < 0x7f, "non-printable or non-ASCII %U in %q", r, line)
			}
		})
	}
}

func TestRunShow_AnnotationWithoutID_PrintsNoIDMarker(t *testing.T) {
	at := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	id := seedShowAnnotations(t, model.Annotation{Author: "a", Text: "t", CreatedAt: at})
	assert.NotContains(t, showOutput(t, id), "(id ")
}

func TestRunResolveAnnotation_PrefixCases_ResolveOrRefuse(t *testing.T) {
	at := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name    string
		ref     string
		wantErr error
	}{
		{"full id", annotationIDOne, nil},
		{"short prefix", annotationIDOne[:12], nil},
		{"ambiguous prefix", annotationIDOne[:12], model.ErrInvalidInput}, // shared by both ids below
		{"too short prefix", annotationIDOne[:8], model.ErrInvalidInput},
		{"no match", "01ZZZZZZZZZZ", model.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			second := annotationIDTwo
			if tt.name != "ambiguous prefix" {
				second = "01L0000000000000000000000B" // differs inside the first 12 characters
			}
			id := seedShowAnnotations(t,
				model.Annotation{ID: annotationIDOne, Author: "a", Text: "one", CreatedAt: at},
				model.Annotation{ID: second, Author: "a", Text: "two", CreatedAt: at.Add(time.Second)})
			err := runResolveAnnotation(id, tt.ref)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestRunResolveAnnotation_PrefixTyped_ReportsFullIDInJSONAndText pins the
// output contract: the full id is reported, not the reference that was typed.
func TestRunResolveAnnotation_PrefixTyped_ReportsFullIDInJSONAndText(t *testing.T) {
	at := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	short := model.ShortAnnotationID(annotationIDOne)
	for _, jsonMode := range []bool{true, false} {
		name := map[bool]string{true: "json", false: "text"}[jsonMode]
		t.Run(name, func(t *testing.T) {
			id := seedShowAnnotations(t, model.Annotation{ID: annotationIDOne, Author: "a", Text: "t", CreatedAt: at})
			app.jsonOutput = jsonMode
			t.Cleanup(func() { app.jsonOutput = false })
			out := captureStdout(t, func() { require.NoError(t, runResolveAnnotation(id, short)) })
			if jsonMode {
				var got map[string]string
				require.NoError(t, json.Unmarshal([]byte(out), &got))
				assert.Equal(t, annotationIDOne, got["annotation_id"])
				assert.Equal(t, "resolved", got["status"])
				return
			}
			assert.Contains(t, out, `"`+annotationIDOne+`"`)
		})
	}
}
