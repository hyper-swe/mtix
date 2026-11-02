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
)

// An addressed comment through mtix serve requests the push after it commits
// and reports the state; an ordinary comment never does (ADR-007 D21,
// MTIX-112.20.2).
func TestCommentNode_AddressedSend_RequestsPush(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantCalls []string
		wantPush  string
	}{
		{"explicit to", `{"text":"hi","to":"agent-b"}`, []string{"agent-b"}, "queued"},
		{"at token", `{"text":"@agent-c please look"}`, []string{"agent-c"}, "queued"},
		{"ordinary", `{"text":"just a note"}`, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t)
			nodeID := createTestNode(t, s, "Hook Test", "TEST")
			var calls []string
			s.SetAddressedSendHook(func(_ context.Context, to string) string {
				calls = append(calls, to)
				return "queued"
			})
			w := httptest.NewRecorder()
			req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/comment", tt.body)
			s.Router().ServeHTTP(w, req)
			require.Equal(t, http.StatusCreated, w.Code)
			assert.Equal(t, tt.wantCalls, calls)
			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			if tt.wantPush == "" {
				assert.NotContains(t, resp, "push")
			} else {
				assert.Equal(t, tt.wantPush, resp["push"])
			}
		})
	}
}

func TestCommentNode_AddressedSendRefused_NoHookCall(t *testing.T) {
	s := testServer(t)
	called := false
	s.SetAddressedSendHook(func(context.Context, string) string { called = true; return "queued" })
	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes/MISSING-1/comment", `{"text":"hi","to":"agent-b"}`)
	s.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.False(t, called, "a failed commit requests no push")
}
