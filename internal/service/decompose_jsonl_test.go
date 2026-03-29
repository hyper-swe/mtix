// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestParseJSONL_ValidLines_ReturnsInputs(t *testing.T) {
	input := strings.NewReader(`{"title":"Add login endpoint","prompt":"Implement POST /auth/login"}
{"title":"Add token refresh","prompt":"Implement POST /auth/refresh","priority":2}
{"title":"Add logout","description":"Clean up sessions"}
`)

	results, err := service.ParseJSONL(input)
	require.NoError(t, err)
	require.Len(t, results, 3)

	assert.Equal(t, "Add login endpoint", results[0].Title)
	assert.Equal(t, "Implement POST /auth/login", results[0].Prompt)

	assert.Equal(t, "Add token refresh", results[1].Title)
	assert.Equal(t, "Implement POST /auth/refresh", results[1].Prompt)
	assert.Equal(t, model.Priority(2), results[1].Priority)

	assert.Equal(t, "Add logout", results[2].Title)
	assert.Equal(t, "Clean up sessions", results[2].Description)
}

func TestParseJSONL_EmptyLines_Skipped(t *testing.T) {
	input := strings.NewReader(`{"title":"Task A"}

{"title":"Task B"}

`)

	results, err := service.ParseJSONL(input)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "Task A", results[0].Title)
	assert.Equal(t, "Task B", results[1].Title)
}

func TestParseJSONL_MissingTitle_ReturnsError(t *testing.T) {
	input := strings.NewReader(`{"title":"Valid task"}
{"prompt":"No title here"}
`)

	_, err := service.ParseJSONL(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 2")
	assert.Contains(t, err.Error(), "title is required")
}

func TestParseJSONL_InvalidJSON_ReturnsError(t *testing.T) {
	input := strings.NewReader(`{"title":"Valid task"}
not valid json
`)

	_, err := service.ParseJSONL(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 2")
}

func TestParseJSONL_EmptyInput_ReturnsError(t *testing.T) {
	input := strings.NewReader("")

	_, err := service.ParseJSONL(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tasks")
}

func TestParseJSONL_AllFields_Parsed(t *testing.T) {
	input := strings.NewReader(`{"title":"Full task","prompt":"Do the thing","acceptance":"Tests pass","description":"Detailed desc","priority":1,"labels":["bug","p0"]}
`)

	results, err := service.ParseJSONL(input)
	require.NoError(t, err)
	require.Len(t, results, 1)

	r := results[0]
	assert.Equal(t, "Full task", r.Title)
	assert.Equal(t, "Do the thing", r.Prompt)
	assert.Equal(t, "Tests pass", r.Acceptance)
	assert.Equal(t, "Detailed desc", r.Description)
	assert.Equal(t, model.Priority(1), r.Priority)
	assert.Equal(t, []string{"bug", "p0"}, r.Labels)
}

func TestParseJSONL_TitleTooLong_ReturnsError(t *testing.T) {
	longTitle := strings.Repeat("x", 501)
	input := strings.NewReader(`{"title":"` + longTitle + `"}`)

	_, err := service.ParseJSONL(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 1")
	assert.Contains(t, err.Error(), "title too long")
}

func TestParseJSONL_WhitespaceOnlyLines_Skipped(t *testing.T) {
	input := strings.NewReader(`
{"title":"Task A"}

{"title":"Task B"}
`)

	results, err := service.ParseJSONL(input)
	require.NoError(t, err)
	require.Len(t, results, 2)
}
