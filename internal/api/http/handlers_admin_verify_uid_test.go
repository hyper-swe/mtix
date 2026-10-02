// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestAdmin_Verify_DuplicateNodeUIDs_ReportsRecovery verifies POST
// /api/v1/admin/verify reports uid_unique_ok true on a clean store and, when
// two nodes share a uid, false with the recovery text (MTIX-95.31.8).
func TestAdmin_Verify_DuplicateNodeUIDs_ReportsRecovery(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for i, id := range []string{"UQ-1", "UQ-2"} {
		require.NoError(t, s.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "UQ", Seq: i + 1, Title: id, Status: model.StatusOpen,
			Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeEpic,
			ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
		}))
	}
	verify := func() map[string]any {
		w := httptest.NewRecorder()
		req := apiRequest(http.MethodPost, "/api/v1/admin/verify", "")
		s.Router().ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		return resp
	}
	assert.Equal(t, true, verify()["uid_unique_ok"])

	_, err := s.store.WriteDB().ExecContext(ctx, `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = s.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET uid = (SELECT uid FROM nodes WHERE id = 'UQ-1') WHERE id = 'UQ-2'`)
	require.NoError(t, err)

	resp := verify()
	assert.Equal(t, false, resp["uid_unique_ok"])
	assert.Contains(t, resp["uid_unique_recovery"], "UQ-1, UQ-2")
	assert.Contains(t, resp["uid_unique_recovery"], "UPDATE nodes SET uid = ''")
}
