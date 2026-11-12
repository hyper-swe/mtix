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
)

// MTIX-255.4: GET /api/v1/search?labels=a,b matches nodes carrying ANY listed label.
func TestAPI_Search_LabelsFilter_AnyOf(t *testing.T) {
	s := testServer(t)
	for _, body := range []string{
		`{"title":"Now one","project":"TEST","labels":["desk:now"]}`,
		`{"title":"Backlog one","project":"TEST","labels":["desk:backlog"]}`,
		`{"title":"Plain","project":"TEST"}`,
	} {
		w := httptest.NewRecorder()
		req := newLocalRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "mtix")
		s.Router().ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code)
	}
	titles := func(query string) []string {
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, newLocalRequest(http.MethodGet, "/api/v1/search?project=all&"+query, nil))
		require.Equal(t, http.StatusOK, w.Code)
		var resp struct {
			Nodes []struct {
				Title string `json:"title"`
			} `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		out := []string{}
		for _, n := range resp.Nodes {
			out = append(out, n.Title)
		}
		return out
	}
	assert.Equal(t, []string{"Now one"}, titles("labels=desk:now"))
	assert.ElementsMatch(t, []string{"Now one", "Backlog one"}, titles("labels=desk:now,desk:backlog"))
	assert.ElementsMatch(t, []string{"Now one", "Backlog one"}, titles("labels=desk:now&labels=desk:backlog"))
	assert.Empty(t, titles("labels=missing"))
	assert.Len(t, titles("labels=%20,"), 3, "blank entries mean no filter")
}
