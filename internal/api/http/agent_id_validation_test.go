// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Agent identity boundaries reject unsafe nonempty IDs without writes (MTIX-125).
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func invalidAgentIDs() []struct{ name, id string } {
	return []struct{ name, id string }{
		{"spaces", "   "}, {"unicode_space", "\u2003\u00a0"},
		{"overlong", strings.Repeat("a", 65)}, {"utf8_bytes", strings.Repeat("é", 33)},
		{"nul", "a\x00b"}, {"tab", "a\tb"}, {"newline", "a\nb"},
		{"del", "a\x7fb"}, {"c1", "a\u0085b"},
		{"zero_width", "a\u200bb"}, {"joiner", "a\u200db"},
		{"bidi", "a\u202eb"}, {"bidi_isolate", "a\u2066b"},
		{"bom", "a\ufeffb"}, {"soft_hyphen", "a\u00adb"},
	}
}

func TestAgentID_RESTBoundaries_RejectBeforeMutation(t *testing.T) {
	for _, op := range []string{"create", "claim", "reclaim", "unclaim"} {
		t.Run(op, func(t *testing.T) {
			for _, tc := range invalidAgentIDs() {
				t.Run(tc.name, func(t *testing.T) {
					s := testServer(t)
					ctx := context.Background()
					n, err := s.nodeWriter.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "existing"})
					require.NoError(t, err)
					if op == "reclaim" || op == "unclaim" {
						require.NoError(t, s.store.ClaimNode(ctx, n.ID, "old"))
					}
					if op == "reclaim" {
						_, err = s.store.WriteDB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`, "2000-01-01T00:00:00Z", "old")
						require.NoError(t, err)
					}
					before, err := s.store.GetNode(ctx, n.ID)
					require.NoError(t, err)
					req := agentIDRESTRequest(t, op, n.ID, tc.id)
					res := httptest.NewRecorder()
					s.Router().ServeHTTP(res, req)
					require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
					require.Contains(t, res.Body.String(), "INVALID_INPUT")
					after, getErr := s.store.GetNode(ctx, n.ID)
					require.NoError(t, getErr)
					require.Equal(t, before, after)
				})
			}
		})
	}
}

func TestAgentID_RESTValidRawAndEmptyCreate_Preserved(t *testing.T) {
	for _, agent := range []string{"", " Agent.User+é ", strings.Repeat("a", 64), strings.Repeat("é", 32)} {
		t.Run(agent, func(t *testing.T) {
			s := testServer(t)
			raw, err := json.Marshal(map[string]string{"project": "TEST", "title": "raw", "assignee": agent})
			require.NoError(t, err)
			req := newLocalRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(string(raw)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "mtix")
			res := httptest.NewRecorder()
			s.Router().ServeHTTP(res, req)
			require.Equal(t, http.StatusCreated, res.Code, res.Body.String())
			var n model.Node
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &n))
			require.Equal(t, agent, n.Assignee)
			if agent != "" {
				for _, action := range []string{"unclaim", "claim"} {
					req := newLocalRequest(http.MethodPost, "/api/v1/nodes/"+n.ID+"/"+action, strings.NewReader(`{"reason":"release"}`))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Requested-With", "mtix")
					if action == "claim" {
						req.Header.Set("X-Agent-ID", agent)
					}
					res := httptest.NewRecorder()
					s.Router().ServeHTTP(res, req)
					require.Equal(t, http.StatusOK, res.Code, res.Body.String())
				}
				got, err := s.store.GetNode(context.Background(), n.ID)
				require.NoError(t, err)
				require.Equal(t, agent, got.Assignee)
			}
		})
	}
}

func agentIDRESTRequest(t *testing.T, op, id, agent string) *http.Request {
	t.Helper()
	path := "/api/v1/nodes/" + id + "/claim"
	body := map[string]any{}
	if op == "create" {
		path = "/api/v1/nodes"
		body = map[string]any{"project": "TEST", "title": "refused", "assignee": agent}
	}
	if op == "reclaim" {
		body["force"] = true
	}
	if op == "unclaim" {
		path = "/api/v1/nodes/" + id + "/unclaim"
		body["reason"] = "release"
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := newLocalRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	// Handler-level injection: the HTTP wire rejects some literal header controls.
	req.Header.Set("X-Agent-ID", agent)
	if op == "create" {
		req.Header.Set("X-Agent-ID", "author")
	}
	return req
}
