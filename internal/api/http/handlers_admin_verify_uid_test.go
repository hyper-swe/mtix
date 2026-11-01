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

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/testutil"
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
	assert.Contains(t, resp["uid_unique_recovery"], `"UQ-1", "UQ-2"`)
	assert.Contains(t, resp["uid_unique_recovery"], "UPDATE nodes SET uid = ''")
}

// TestAdmin_Verify_HostileHolderIDs checks the shared recovery display at the REST boundary.
func TestAdmin_Verify_HostileHolderIDs(t *testing.T) {
	for _, fixture := range testutil.HostileIDs() {
		t.Run(fixture.Name, func(t *testing.T) {
			s := testServer(t)
			ctx := context.Background()
			for i, id := range []string{"TEST-1", "TEST-2"} {
				applyHostileEvent(t, s.store, id, id, model.OpCreateNode, &model.CreateNodePayload{Title: "Holder"}, int64(i+1))
			}
			_, err := s.store.WriteDB().ExecContext(ctx, `DROP INDEX idx_nodes_uid`)
			require.NoError(t, err)
			_, err = s.store.WriteDB().ExecContext(ctx, `UPDATE nodes SET id = ?, uid = ? WHERE id = ?`, fixture.Value, "shared-uid", "TEST-1")
			require.NoError(t, err)
			_, err = s.store.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = ?`, "shared-uid", "TEST-2")
			require.NoError(t, err)
			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, apiRequest(http.MethodPost, "/api/v1/admin/verify", ""))
			require.Equal(t, http.StatusOK, w.Code)
			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, false, resp["uid_unique_ok"])
			recovery, ok := resp["uid_unique_recovery"].(string)
			require.True(t, ok)
			assert.True(t, isASCIIText(recovery), "recovery must be printable ASCII")
			assert.Contains(t, recovery, format.EventID(fixture.Value))
			var stored string
			require.NoError(t, s.store.WriteDB().QueryRowContext(ctx, `SELECT id FROM nodes WHERE id = ?`, fixture.Value).Scan(&stored))
			assert.Equal(t, fixture.Value, stored)
			assertHolderJSONEncoding(t, s, fixture.Value)
			// Structured JSON identifiers retain their original value.
			node := hostileGet(s, "/api/v1/nodes/TEST-2")
			require.Equal(t, http.StatusOK, node.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(node.Body.Bytes(), &body))
			assert.Equal(t, "TEST-2", body["id"])
			assert.Equal(t, "shared-uid", body["uid"])
		})
	}
}

// assertHolderJSONEncoding compares structured IDs with ordinary JSON encoding,
// including encoding/json's replacement of invalid UTF-8; display quoting must
// never be applied to an identifier field.
func assertHolderJSONEncoding(t *testing.T, s *serverFixture, id string) {
	t.Helper()
	w := hostileGet(s, "/api/v1/search?project=all")
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Nodes []struct {
			ID json.RawMessage `json:"id"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	want, err := json.Marshal(id)
	require.NoError(t, err)
	ids := make([]string, 0, len(body.Nodes))
	for _, node := range body.Nodes {
		ids = append(ids, string(node.ID))
	}
	assert.Contains(t, ids, string(want))
}
