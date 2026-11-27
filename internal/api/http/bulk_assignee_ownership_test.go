// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// bulkAssigneeCall sends PATCH /bulk/nodes with a body and acting agent.
func bulkAssigneeCall(t *testing.T, s *serverFixture, agent, body string) (bool, string) {
	t.Helper()
	req := apiRequest(http.MethodPatch, "/api/v1/bulk/nodes", body)
	if agent != "" {
		req.Header.Set("X-Agent-ID", agent)
	}
	res := httptest.NewRecorder()
	s.Router().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		return false, res.Body.String()
	}
	var out struct {
		Results []struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &out))
	require.Len(t, out.Results, 1)
	return out.Results[0].Success, out.Results[0].Error
}

func claimedRESTNode(t *testing.T, s *serverFixture, agent string) string {
	t.Helper()
	id := createTestNode(t, s, "Claimed", "TEST")
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+id+"/claim", `{}`)
	req.Header.Set("X-Agent-ID", agent)
	res := httptest.NewRecorder()
	s.Router().ServeHTTP(res, req)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	return id
}

// TestBulkUpdate_AssigneeOwnership verifies PATCH /bulk/nodes cannot clear or
// change another agent's claim without force, records the forced change, and
// lets the assignee through (MTIX-325).
func TestBulkUpdate_AssigneeOwnership(t *testing.T) {
	s := testServer(t)
	id := claimedRESTNode(t, s, "agent-a")
	clearBody := `{"updates":[{"id":"` + id + `","fields":{"assignee":""}}]`

	for _, agent := range []string{"agent-b", ""} {
		ok, msg := bulkAssigneeCall(t, s, agent, clearBody+`}`)
		assert.False(t, ok, "agent %q", agent)
		assert.Contains(t, msg, "agent-a")
	}
	ok, _ := bulkAssigneeCall(t, s, "agent-b", clearBody+`,"force":true}`)
	assert.False(t, ok, "force without reason must be refused")
	n, err := s.store.GetNode(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "agent-a", n.Assignee)

	ok, msg := bulkAssigneeCall(t, s, "agent-b", clearBody+`,"force":true,"reason":"host crashed"}`)
	require.True(t, ok, msg)
	entries, err := s.store.GetActivity(t.Context(), id, 100, 0)
	require.NoError(t, err)
	var found bool
	for _, e := range entries {
		if e.Type == model.ActivityTypeUnclaim && e.Author == "agent-b" &&
			strings.Contains(e.Text, "held by agent-a: host crashed") {
			found = true
		}
	}
	assert.True(t, found, "forced audit entry missing")

	id2 := claimedRESTNode(t, s, "agent-a")
	ok, msg = bulkAssigneeCall(t, s, "agent-a", `{"updates":[{"id":"`+id2+`","fields":{"assignee":""}}]}`)
	assert.True(t, ok, msg)
}
