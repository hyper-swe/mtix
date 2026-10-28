// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// hostileParentRequest creates a node under a parent id the caller made up, so
// the service answers with a not-found error that repeats that id.
func hostileParentRequest(t *testing.T, id string) *http.Request {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"project": "TEST", "title": "t", "parent_id": id})
	require.NoError(t, err)
	req := newLocalRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	req.Header.Set("X-Agent-ID", "author")
	return req
}

func assertPrintableMessage(t *testing.T, msg string) {
	t.Helper()
	require.True(t, utf8.ValidString(msg), "message is valid UTF-8: %q", msg)
	for _, r := range msg {
		if format.SafeText(string(r)) != string(r) {
			t.Fatalf("unsafe rune %U in REST message %q", r, msg)
		}
	}
}

func decodeError(t *testing.T, res *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body), "body must be valid JSON: %q", res.Body.String())
	return body
}

func TestRESTNotFound_HostileID_MessageIsPrintableSafe(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 8192 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			s := testServer(t)
			res := httptest.NewRecorder()
			s.Router().ServeHTTP(res, hostileParentRequest(t, f.Value))
			require.GreaterOrEqual(t, res.Code, 400, res.Body.String())
			assertPrintableMessage(t, decodeError(t, res).Error.Message)
		})
	}
}

func TestRESTNotFound_GetPath_HostileID_MessageIsPrintableSafe(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 2048 || strings.ContainsAny(f.Value, "/\\.") {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			s := testServer(t)
			req := newLocalRequest(http.MethodGet, "/api/v1/nodes/"+url.PathEscape(f.Value), nil)
			res := httptest.NewRecorder()
			s.Router().ServeHTTP(res, req)
			if res.Code == http.StatusNotFound && !strings.HasPrefix(res.Header().Get("Content-Type"), "application/json") {
				t.Skip("routing 404 before any handler runs")
			}
			assertPrintableMessage(t, decodeError(t, res).Error.Message)
		})
	}
}

func TestRESTErrors_HostileIDs_ContentTypeAndNoSniff(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 8192 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			s := testServer(t)
			res := httptest.NewRecorder()
			s.Router().ServeHTTP(res, hostileParentRequest(t, f.Value))
			require.GreaterOrEqual(t, res.Code, 400)
			assert.True(t, strings.HasPrefix(res.Header().Get("Content-Type"), "application/json"), res.Header().Get("Content-Type"))
			assert.Equal(t, "nosniff", res.Header().Get("X-Content-Type-Options"))
			assert.True(t, json.Valid(res.Body.Bytes()), "valid JSON body")
		})
	}
}

func TestHandleValidationError_HostileMessage_IsEscaped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 8192 {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			HandleValidationError(c, "field "+f.Value+" is invalid")
			require.Equal(t, http.StatusBadRequest, w.Code)
			assertPrintableMessage(t, decodeError(t, w).Error.Message)
			assert.Equal(t, format.SafeText("field "+f.Value+" is invalid"), decodeError(t, w).Error.Message)
		})
	}
}

// TestBulkUpdate_HostileID_ItemErrorIsEscapedAndIDKeptExact proves the per-item
// error text of PATCH /api/v1/bulk/nodes is printable-safe while the id field
// still echoes the caller's exact value.
func TestBulkUpdate_HostileID_ItemErrorIsEscapedAndIDKeptExact(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		if f.Value == "" || len(f.Value) > 8192 || !utf8.ValidString(f.Value) {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			s := testServer(t)
			raw, err := json.Marshal(map[string]any{"updates": []map[string]any{
				{"id": f.Value, "fields": map[string]any{"title": "x"}},
			}})
			require.NoError(t, err)
			res := httptest.NewRecorder()
			s.Router().ServeHTTP(res, apiRequest(http.MethodPatch, "/api/v1/bulk/nodes", string(raw)))
			require.Equal(t, http.StatusOK, res.Code, res.Body.String())
			var body struct {
				Results []struct {
					ID      string `json:"id"`
					Success bool   `json:"success"`
					Error   string `json:"error"`
				} `json:"results"`
			}
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
			require.Len(t, body.Results, 1)
			require.False(t, body.Results[0].Success)
			require.NotEmpty(t, body.Results[0].Error)
			assertPrintableMessage(t, body.Results[0].Error)
			assert.Equal(t, f.Value, body.Results[0].ID)
		})
	}
}
