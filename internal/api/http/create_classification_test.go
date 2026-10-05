// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// REST creation retains header author independently of explicit assignment.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestCreateREST_ClassificationAndAssignment(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			s := testServer(t)
			raw, err := json.Marshal(map[string]string{"title": "Classified work", "project": "TEST", "creator": "body-author", "issue_type": kind, "assignee": "worker"})
			require.NoError(t, err)
			req := newLocalRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(string(raw)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "mtix")
			req.Header.Set("X-Agent-ID", "author")
			res := httptest.NewRecorder()
			s.Router().ServeHTTP(res, req)
			if kind == "invalid" {
				require.Equal(t, http.StatusBadRequest, res.Code)
				assert.Contains(t, res.Body.String(), "refactor")
				return
			}
			require.Equal(t, http.StatusCreated, res.Code, res.Body.String())
			var n model.Node
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &n))
			assert.Equal(t, model.IssueType(kind), n.IssueType)
			assert.Equal(t, model.NodeTypeEpic, n.NodeType)
			assert.Equal(t, "worker", n.Assignee)
			assert.Equal(t, "author", n.Creator)
			assert.Equal(t, model.StatusInProgress, n.Status)
			got, err := s.store.GetNode(context.Background(), n.ID)
			require.NoError(t, err)
			assert.Equal(t, n.IssueType, got.IssueType)
			assert.Equal(t, n.Assignee, got.Assignee)
		})
	}
}
