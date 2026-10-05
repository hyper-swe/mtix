// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

// REST dependency parity uses real routing and SQLite persistence for every model type.
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestAPI_Dependencies_AllModelTypesPersistAndRemove(t *testing.T) {
	for _, depType := range model.AllDepTypes() {
		t.Run(string(depType), func(t *testing.T) {
			s := testServer(t)
			from := createTestNode(t, s, "Dependency source", "TEST")
			to := createTestNode(t, s, "Dependency target", "TEST")
			body, err := json.Marshal(map[string]string{"from_id": from, "to_id": to, "dep_type": string(depType)})
			require.NoError(t, err)
			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, apiRequest(http.MethodPost, "/api/v1/deps", string(body)))
			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			var count int
			// Inspect exact persisted type, including informational edges.
			query := "SELECT count(*) FROM dependencies WHERE from_id = ? AND to_id = ? AND dep_type = ?"
			require.NoError(t, s.store.ReadDB().QueryRowContext(context.Background(), query, from, to, depType).Scan(&count))
			assert.Equal(t, 1, count)
			w = httptest.NewRecorder()
			s.Router().ServeHTTP(w, apiRequest(http.MethodDelete, "/api/v1/deps/"+from+"/"+to+"?dep_type="+string(depType), ""))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.NoError(t, s.store.ReadDB().QueryRowContext(context.Background(), query, from, to, depType).Scan(&count))
			assert.Zero(t, count)
		})
	}
}
