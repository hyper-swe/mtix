// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// postDecompose posts a decompose body and returns the recorder.
func postDecompose(t *testing.T, s *serverFixture, parentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, apiRequest(http.MethodPost, "/api/v1/nodes/"+parentID+"/decompose", body))
	return w
}

// TestAPI_DecomposeNode_BadLaterChild_RefusedBeforeAnyCreate proves the whole
// batch is validated before the first create (MTIX-279): a bad later child is
// a 400 and the earlier valid child is not created.
func TestAPI_DecomposeNode_BadLaterChild_RefusedBeforeAnyCreate(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"priority above range", `{"children":[{"title":"ok"},{"title":"bad","priority":9}]}`},
		{"priority below range", `{"children":[{"title":"ok"},{"title":"bad","priority":-1}]}`},
		{"empty title", `{"children":[{"title":"ok"},{"title":""}]}`},
		{"title too long", `{"children":[{"title":"ok"},{"title":"` + longTitle() + `"}]}`},
		{"invalid issue type", `{"children":[{"title":"ok"},{"title":"bad","issue_type":"epic"}]}`},
		{"description too long", `{"children":[{"title":"ok"},{"title":"bad","description":"` + bigString(model.MaxDescriptionSize+1) + `"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t)
			parentID := createTestNode(t, s, "Decompose Parent", "TEST")
			w := postDecompose(t, s, parentID, tt.body)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			kids, err := s.nodeSvc.GetDirectChildren(context.Background(), parentID)
			require.NoError(t, err)
			assert.Empty(t, kids, "a refused batch creates nothing")
		})
	}
}

func longTitle() string { return bigString(model.MaxTitleLength + 1) }

func bigString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

// TestAPI_DecomposeNode_AppliesDescriptionPriorityLabels proves the REST path
// stores every documented child key.
func TestAPI_DecomposeNode_AppliesDescriptionPriorityLabels(t *testing.T) {
	s := testServer(t)
	parentID := createTestNode(t, s, "Decompose Parent", "TEST")
	w := postDecompose(t, s, parentID,
		`{"children":[{"title":"a","description":"why","priority":2,"labels":["x","y"],"prompt":"p","acceptance":"ac","type":"bug"}]}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	kids, err := s.nodeSvc.GetDirectChildren(context.Background(), parentID)
	require.NoError(t, err)
	require.Len(t, kids, 1)
	assert.Equal(t, "why", kids[0].Description)
	assert.Equal(t, model.Priority(2), kids[0].Priority)
	assert.Equal(t, []string{"x", "y"}, kids[0].Labels)
	assert.Equal(t, "p", kids[0].Prompt)
	assert.Equal(t, "ac", kids[0].Acceptance)
	assert.Equal(t, model.IssueTypeBug, kids[0].IssueType)
}

// TestAPI_DecomposeNode_UnknownKey_NamesChildAndKey proves an unknown key is a
// 400 that names the child index and the key, and creates nothing.
func TestAPI_DecomposeNode_UnknownKey_NamesChildAndKey(t *testing.T) {
	s := testServer(t)
	parentID := createTestNode(t, s, "Decompose Parent", "TEST")
	w := postDecompose(t, s, parentID, `{"children":[{"title":"ok"},{"title":"x","severity":"high"}]}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, w.Body.String(), "child 1")
	assert.Contains(t, w.Body.String(), "severity")
	kids, err := s.nodeSvc.GetDirectChildren(context.Background(), parentID)
	require.NoError(t, err)
	assert.Empty(t, kids)
}

// TestAPI_DecomposeNode_WrongTypeKey_NoInternalTypeName proves a type
// mismatch is refused without leaking Go type names.
func TestAPI_DecomposeNode_WrongTypeKey_NoInternalTypeName(t *testing.T) {
	s := testServer(t)
	parentID := createTestNode(t, s, "Decompose Parent", "TEST")
	w := postDecompose(t, s, parentID, `{"children":[{"title":"ok"},{"title":"x","priority":"high"}]}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "child 1")
	assert.Contains(t, w.Body.String(), "priority")
	assert.NotContains(t, w.Body.String(), "Go struct field")
	assert.NotContains(t, w.Body.String(), "decomposeInputWire")
}
