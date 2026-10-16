// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// REST reads use the cache after CLI startup; handlers do not auto-import.
// Native tasks.json ownership must not prevent database reads (FR-15.8, MTIX-149).
func TestRESTRead_SyncLockHeld_ReturnsNodeData(t *testing.T) {
	s := testServer(t)
	node, err := s.nodeWriter.CreateNode(t.Context(), &service.CreateNodeRequest{
		Project: "TEST", Title: "REST cached node", Description: "data remains readable",
	})
	require.NoError(t, err)
	dir := t.TempDir()
	syncSvc := service.NewSyncService(s.store, s.logger, testClock())
	require.NoError(t, syncSvc.ForceExport(t.Context(), dir))
	lock, err := pushlock.AcquireNamed(dir, "sync.lock")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })
	contender, err := pushlock.AcquireNamed(dir, "sync.lock")
	require.ErrorIs(t, err, pushlock.ErrLockHeld)
	require.Nil(t, contender)

	response := httptest.NewRecorder()
	s.Router().ServeHTTP(response, newLocalRequest(http.MethodGet, "/api/v1/nodes/"+node.ID, nil))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var got model.Node
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
	require.Equal(t, node.ID, got.ID)
	require.Equal(t, node.Title, got.Title)
	require.Equal(t, node.Description, got.Description)
}
