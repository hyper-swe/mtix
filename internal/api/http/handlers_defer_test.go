// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-95.22: POST /api/v1/nodes/:id/defer validated `until` and dropped it,
// and it ignored a request body that is not valid JSON.

// storedDeferUntil reads the node's defer_until column verbatim.
func storedDeferUntil(t *testing.T, s *Server, id string) sql.NullString {
	t.Helper()
	var v sql.NullString
	require.NoError(t, s.store.QueryRow(context.Background(),
		`SELECT defer_until FROM nodes WHERE id = ?`, id).Scan(&v))
	return v
}

// storedStatus reads the node's status through the service.
func storedStatus(t *testing.T, s *Server, id string) model.Status {
	t.Helper()
	node, err := s.nodeSvc.GetNode(context.Background(), id)
	require.NoError(t, err)
	return node.Status
}

// TestDefer_WithUntil_StoresDeferUntil verifies the HTTP defer stores `until`
// as UTC RFC 3339 (MTIX-95.22, FR-3.8b).
func TestDefer_WithUntil_StoresDeferUntil(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"utc timestamp", `{"until":"2031-01-01T00:00:00Z"}`, "2031-01-01T00:00:00Z"},
		{"offset timestamp is stored in utc", `{"until":"2031-01-01T02:00:00+02:00"}`, "2031-01-01T00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t)
			nodeID := createTestNode(t, s, "Defer Until", "TEST")

			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/defer", tt.body))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			got := storedDeferUntil(t, s, nodeID)
			require.True(t, got.Valid, "until must be stored, not dropped")
			assert.Equal(t, tt.want, got.String)
			assert.Equal(t, model.StatusDeferred, storedStatus(t, s, nodeID))
		})
	}
}

// TestDeferNode_MalformedJSON_Returns400 verifies a body that is not valid
// JSON, or whose until is not a string, is rejected with 400 and the node is
// left untouched, instead of being ignored (MTIX-95.22).
func TestDeferNode_MalformedJSON_Returns400(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not json", `defer me`},
		{"truncated object", `{"until":`},
		{"until is not a string", `{"until":20310101}`},
		{"array instead of object", `["2031-01-01T00:00:00Z"]`},
		// MTIX-95.22 round 2: only an object whose sole key is until.
		{"literal null", `null`},
		{"misspelled key", `{"untill":"2031-01-01T00:00:00Z"}`},
		{"unknown key beside until", `{"until":"2031-01-01T00:00:00Z","reason":"later"}`},
		{"data after the object", `{"until":"2031-01-01T00:00:00Z"} {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t)
			nodeID := createTestNode(t, s, "Defer Malformed", "TEST")

			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/defer", tt.body))

			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Equal(t, model.StatusOpen, storedStatus(t, s, nodeID),
				"a rejected defer must not transition")
		})
	}
}

// TestDeferNode_EmptyBody_DefersWithoutWakeTime verifies a request with no
// body, a blank body or an empty object still defers, with no wake time
// (MTIX-95.22).
func TestDeferNode_EmptyBody_DefersWithoutWakeTime(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"no body", ""},
		{"blank body", " \n\t"},
		{"empty object", `{}`},
		{"empty until", `{"until":""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t)
			nodeID := createTestNode(t, s, "Defer Empty", "TEST")

			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/defer", tt.body))

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, model.StatusDeferred, storedStatus(t, s, nodeID))
			assert.False(t, storedDeferUntil(t, s, nodeID).Valid)
		})
	}
}

// TestDecodeDeferBody_NilBody_MeansNoUntil verifies a request with no body
// reader at all is read as a defer with no wake time (MTIX-95.22).
func TestDecodeDeferBody_NilBody_MeansNoUntil(t *testing.T) {
	until, err := decodeDeferBody(nil)
	require.NoError(t, err)
	assert.Empty(t, until)
}

// TestDeferNode_Author_IsAgentHeaderOrAPI verifies the HTTP defer records the
// X-Agent-ID header as the author of the activity entry and the sync event,
// and "api" without one (MTIX-95.22 round 3).
func TestDeferNode_Author_IsAgentHeaderOrAPI(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"with X-Agent-ID", "agent-h", "agent-h"},
		{"without X-Agent-ID", "", "api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t)
			nodeID := createTestNode(t, s, "Defer Author", "TEST")
			req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/defer", `{"until":"2031-01-01T00:00:00Z"}`)
			if tt.header != "" {
				req.Header.Set("X-Agent-ID", tt.header)
			}

			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			ctx := context.Background()
			entries, err := s.store.GetActivity(ctx, nodeID, 100, 0)
			require.NoError(t, err)
			require.NotEmpty(t, entries)
			assert.Equal(t, tt.want, entries[len(entries)-1].Author, "activity author")
			var author string
			require.NoError(t, s.store.QueryRow(ctx,
				`SELECT author_id FROM sync_events WHERE node_id = ? AND op_type = ?`,
				nodeID, string(model.OpTransitionStatus)).Scan(&author))
			assert.Equal(t, tt.want, author, "sync event author")
		})
	}
}
