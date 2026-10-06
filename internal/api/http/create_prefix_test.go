// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Local create boundaries share FR-2.1a prefix validation (MTIX-107.3).
package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestCreateREST_PrefixGrammar_MapsSharedError(t *testing.T) {
	for _, prefix := range []string{"TEST_BAD", "test", "1TEST", "ABCDEFGHIJKLMNOPQRSTU", "TEST%", "A", "TEST-DEV-OPS", "ABCDEFGHIJKLMNOPQRST", ""} {
		t.Run(prefix, func(t *testing.T) {
			s := testServer(t)
			body, err := json.Marshal(map[string]string{"title": "Prefix boundary", "project": prefix})
			require.NoError(t, err)
			res := httptest.NewRecorder()
			s.Router().ServeHTTP(res, apiRequest(http.MethodPost, "/api/v1/nodes", string(body)))
			expectedErr := model.ValidatePrefix(prefix)
			if prefix != "" && expectedErr != nil {
				require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
				var response ErrorResponse
				require.NoError(t, json.Unmarshal(res.Body.Bytes(), &response))
				assert.Equal(t, "INVALID_INPUT", response.Error.Code)
				assert.Equal(t, expectedErr.Error(), response.Error.Message)
				projects, err := s.store.DistinctProjects(t.Context())
				require.NoError(t, err)
				assert.Empty(t, projects)
				return
			}
			require.Equal(t, http.StatusCreated, res.Code, res.Body.String())
			var n model.Node
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &n))
			if prefix == "" {
				prefix = "PROJ"
			}
			assert.Equal(t, prefix, n.Project)
			assert.Equal(t, prefix+"-1", n.ID)
		})
	}
}

func TestCreateREST_InvalidPrimaryPrefix_ReturnsSharedError(t *testing.T) {
	s := testServer(t)
	_, err := s.configWriter.Set("prefix", "TEST_BAD")
	require.NoError(t, err)
	for _, body := range []string{`{"title":"Missing project"}`, `{"title":"Empty project","project":""}`} {
		res := httptest.NewRecorder()
		s.Router().ServeHTTP(res, apiRequest(http.MethodPost, "/api/v1/nodes", body))
		require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
		var response ErrorResponse
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &response))
		assert.Equal(t, "INVALID_INPUT", response.Error.Code)
		assert.Equal(t, model.ValidatePrefix("TEST_BAD").Error(), response.Error.Message)
	}
}

func TestCreateREST_InvalidInheritedPrefix_RejectsValidSuppliedProject(t *testing.T) {
	s := testServer(t)
	st := s.store
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, st.CreateNode(t.Context(), &model.Node{
		ID: "TEST_BAD-1", Project: "TEST_BAD", Seq: 1, Title: "Legacy parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1,
		NodeType: model.NodeTypeEpic, CreatedAt: now, UpdatedAt: now,
	}))
	res := httptest.NewRecorder()
	s.Router().ServeHTTP(res, apiRequest(http.MethodPost, "/api/v1/nodes", `{"title":"Local child","parent_id":"TEST_BAD-1","project":"TEST"}`))
	require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
	var response ErrorResponse
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &response))
	assert.Equal(t, "INVALID_INPUT", response.Error.Code)
	assert.Contains(t, response.Error.Message, "invalid inherited project prefix")
	assert.Contains(t, response.Error.Message, "TEST_BAD")
	_, err := st.GetNode(t.Context(), "TEST_BAD-1.1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}
