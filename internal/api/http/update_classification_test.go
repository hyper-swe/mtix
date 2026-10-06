// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// REST PATCH classification preserves omission and explicit clears.
package http

import (
	"context"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateREST_IssueType_PartialValidated(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "", "invalid", "omitted"} {
		t.Run(kind, func(t *testing.T) {
			s := testServer(t)
			ctx := context.Background()
			n, err := s.nodeWriter.CreateNode(ctx, &service.CreateNodeRequest{Title: "Classified work", Project: "TEST", IssueType: model.IssueTypeFeature})
			require.NoError(t, err)
			fields := map[string]string{"title": "Updated title"}
			if kind != "omitted" {
				fields["issue_type"] = kind
			}
			raw, err := json.Marshal(fields)
			require.NoError(t, err)
			req := newLocalRequest(http.MethodPatch, "/api/v1/nodes/"+n.ID, strings.NewReader(string(raw)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "mtix")
			res := httptest.NewRecorder()
			s.Router().ServeHTTP(res, req)
			if kind == "invalid" {
				require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
			} else {
				require.Equal(t, http.StatusOK, res.Code, res.Body.String())
			}
			got, err := s.store.GetNode(ctx, n.ID)
			require.NoError(t, err)
			want := kind
			if kind == "omitted" || kind == "invalid" {
				want = "feature"
			}
			assert.Equal(t, model.IssueType(want), got.IssueType)
			if kind == "invalid" {
				assert.Equal(t, n.Title, got.Title)
			}
		})
	}
}
