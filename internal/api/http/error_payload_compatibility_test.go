// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// The backend's existing message, including its original context, is public API.
func TestHTTPServiceBoundary_PreservesExactErrorPayload(t *testing.T) {
	for _, name := range []string{"claim missing", "force missing", "unclaim missing", "cancel missing", "claim invalid transition", "dependency cycle", "annotation missing"} {
		t.Run(name, func(t *testing.T) {
			s := testServer(t)
			ctx := context.Background()
			path, body := "/nodes/MISSING-1/claim", `{}`
			var backend error
			switch name {
			case "claim missing":
				backend = s.store.ClaimNode(ctx, "MISSING-1", "writer")
			case "force missing":
				body = `{"force":true}`
				backend = s.store.ForceReclaimNode(ctx, "MISSING-1", "writer", 0)
			case "unclaim missing":
				path, body = "/nodes/MISSING-1/unclaim", `{"reason":"release"}`
				backend = s.store.UnclaimNode(ctx, "MISSING-1", "release", "writer")
			case "cancel missing":
				path, body = "/nodes/MISSING-1/cancel", `{"reason":"cancel"}`
				backend = s.store.CancelNode(ctx, "MISSING-1", "cancel", "writer", false)
			case "annotation missing":
				path, body = "/nodes/MISSING-1/comment", `{"text":"hello"}`
				_, backend = s.store.GetNode(ctx, "MISSING-1")
			case "claim invalid transition":
				createTestNode(t, s, "first", "TEST")
				require.NoError(t, s.store.CancelNode(ctx, "TEST-1", "cancel", "writer", false))
				path = "/nodes/TEST-1/claim"
				backend = s.store.ClaimNode(ctx, "TEST-1", "writer")
			case "dependency cycle":
				createTestNode(t, s, "first", "TEST")
				createTestNode(t, s, "second", "TEST")
				require.NoError(t, s.store.AddDependency(ctx, &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: testClock()()}))
				backend = s.store.AddDependency(ctx, &model.Dependency{FromID: "TEST-2", ToID: "TEST-1", DepType: model.DepTypeBlocks, CreatedAt: testClock()()})
				path, body = "/deps", `{"from_id":"TEST-2","to_id":"TEST-1","dep_type":"blocks"}`
			}
			require.Error(t, backend)
			request := newLocalRequest("POST", "/api/v1"+path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Requested-With", "mtix")
			request.Header.Set("X-Agent-ID", "writer")
			result := httptest.NewRecorder()
			s.Router().ServeHTTP(result, request)
			var actual ErrorResponse
			require.NoError(t, json.Unmarshal(result.Body.Bytes(), &actual))
			require.Equal(t, backend.Error(), actual.Error.Message)
			for _, mapping := range sentinelMapping {
				if errors.Is(backend, mapping.err) {
					require.Equal(t, mapping.code, actual.Error.Code)
					require.Equal(t, mapping.status, result.Code)
					return
				}
			}
			t.Fatalf("unrecognized mapping: %s", result.Body.String())
		})
	}
}
