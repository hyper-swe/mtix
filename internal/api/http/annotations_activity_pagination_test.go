// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func readAnnotationActivityPage(t *testing.T, s *serverFixture, id string, limit, offset int) []map[string]any {
	t.Helper()
	out := httptest.NewRecorder()
	s.Router().ServeHTTP(out, newLocalRequest("GET", fmt.Sprintf("/api/v1/nodes/%s/activity?limit=%d&offset=%d", id, limit, offset), nil))
	require.Equal(t, 200, out.Code, out.Body.String())
	var response struct {
		Entries []map[string]any `json:"entries"`
		Total   int              `json:"total"`
	}
	require.NoError(t, json.Unmarshal(out.Body.Bytes(), &response))
	require.Equal(t, len(response.Entries), response.Total)
	return response.Entries
}

func TestGetActivity_AnnotationPageBoundaries_NoDropsOrRepeats(t *testing.T) {
	s := testServer(t)
	defer s.wsHub.Close()
	id := createTestNode(t, s, "conversation", "TEST")
	for i := 0; i < 43; i++ {
		recipient := ""
		if i%2 == 0 {
			recipient = "reader"
		}
		ann := model.Annotation{ID: fmt.Sprintf("%026d", i+1), Author: "writer", Addressee: recipient, Text: fmt.Sprintf("comment %d", i), CreatedAt: testClock()().Add(time.Duration(i) * time.Second)}
		require.NoError(t, s.nodeWriter.AppendAnnotation(t.Context(), id, ann))
	}
	all := readAnnotationActivityPage(t, s, id, 100, 0)
	require.Len(t, all, 44)
	var combined []map[string]any
	for offset := 0; offset < len(all); offset += 20 {
		combined = append(combined, readAnnotationActivityPage(t, s, id, 20, offset)...)
	}
	require.Equal(t, all, combined)
	seen := map[string]bool{}
	for _, entry := range combined {
		id, ok := entry["id"].(string)
		require.True(t, ok)
		require.False(t, seen[id], "repeated id %s", id)
		seen[id] = true
	}
	require.Empty(t, readAnnotationActivityPage(t, s, id, 20, len(all)))
}

func TestGetActivity_MissingNode_Returns404(t *testing.T) {
	s := testServer(t)
	defer s.wsHub.Close()
	out := httptest.NewRecorder()
	s.Router().ServeHTTP(out, newLocalRequest("GET", "/api/v1/nodes/MISSING-1/activity", nil))
	require.Equal(t, 404, out.Code)
}
