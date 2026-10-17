// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// TestUnclaimNode_REST_RefusesNonAssigneeAndMissingAgent verifies the REST
// unclaim refuses a non-assignee or an unidentified caller (MTIX-169).
func TestUnclaimNode_REST_RefusesNonAssigneeAndMissingAgent(t *testing.T) {
	tests := []struct {
		name   string
		agent  string
		status int
		code   string
	}{
		{"non_assignee", "agent-b", http.StatusConflict, "ALREADY_CLAIMED"},
		{"missing_agent", "", http.StatusBadRequest, "INVALID_INPUT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			ctx := context.Background()
			n, err := s.nodeWriter.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "claimed"})
			require.NoError(t, err)
			require.NoError(t, s.store.ClaimNode(ctx, n.ID, "agent-a"))

			res := restUnclaim(t, s, n.ID, tc.agent, `{"reason":"taking over"}`)

			require.Equal(t, tc.status, res.Code, res.Body.String())
			require.Contains(t, res.Body.String(), tc.code)
			got, err := s.store.GetNode(ctx, n.ID)
			require.NoError(t, err)
			require.Equal(t, "agent-a", got.Assignee)
		})
	}
}

// TestUnclaimNode_REST_ForceByNonAssignee_Returns200 verifies force releases
// another agent's claim over REST (MTIX-169).
func TestUnclaimNode_REST_ForceByNonAssignee_Returns200(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	n, err := s.nodeWriter.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "claimed"})
	require.NoError(t, err)
	require.NoError(t, s.store.ClaimNode(ctx, n.ID, "agent-a"))

	res := restUnclaim(t, s, n.ID, "agent-b", `{"reason":"taking over","force":true}`)

	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	got, err := s.store.GetNode(ctx, n.ID)
	require.NoError(t, err)
	require.Empty(t, got.Assignee)
}

// restUnclaim posts body to the unclaim route as agent (no header when empty).
func restUnclaim(t *testing.T, s *serverFixture, id, agent, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := newLocalRequest(http.MethodPost, "/api/v1/nodes/"+id+"/unclaim", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	if agent != "" {
		req.Header.Set("X-Agent-ID", agent)
	}
	res := httptest.NewRecorder()
	s.Router().ServeHTTP(res, req)
	return res
}
