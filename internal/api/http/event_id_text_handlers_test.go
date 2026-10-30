// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// hostileSyncID is a sync event id with a terminal escape, a newline and a
// bidirectional override. A pulled create event becomes a node uid and a
// pulled comment event an annotation id (ADR-003).
const hostileSyncID = "evt\x1b[31mred\x1b[0m\nINJECTED-\u202eg-\u00e9\u4e16"

const hostileAnnotationID = hostileSyncID + "-note"

func applyHostileEvent(t *testing.T, st *sqlite.Store, id, nodeID string, op model.OpType, payload any, lamport int64) {
	t.Helper()
	pl, err := model.EncodePayload(payload)
	require.NoError(t, err)
	ev := &model.SyncEvent{
		EventID: id, ProjectPrefix: "HS", NodeID: nodeID, OpType: op, Payload: pl,
		WallClockTS: lamport, LamportClock: lamport, VectorClock: model.VectorClock{"remote": lamport},
		AuthorID: "remote", AuthorMachineHash: "0123456789abcdef", SyncStatus: model.SyncStatusApplied,
	}
	require.NoError(t, st.WithTx(context.Background(), func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(context.Background(), tx, ev)
	}))
}

func seedHostileNode(t *testing.T, s *serverFixture) {
	t.Helper()
	applyHostileEvent(t, s.store, hostileSyncID, "HS-1", model.OpCreateNode, &model.CreateNodePayload{Title: "Synced"}, 1)
	applyHostileEvent(t, s.store, hostileAnnotationID, "HS-1", model.OpComment,
		&model.CommentPayload{AuthorID: "remote", Body: "pulled"}, 2)
}

func isASCIIText(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// assertMessageFieldsSafe walks decoded JSON and requires every human-readable
// text field to be printable ASCII; other fields carry ids JSON-encoded.
// Successful responses rarely carry those fields, so the route sweep also
// calls assertNoControlBytes on the raw body.
func assertMessageFieldsSafe(t *testing.T, v any, path string) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if str, ok := child.(string); ok && (k == "message" || k == "detail" || strings.HasSuffix(k, "_recovery")) {
				assert.True(t, isASCIIText(str), "%s.%s = %q", path, k, str)
			}
			assertMessageFieldsSafe(t, child, path+"."+k)
		}
	case []any:
		for _, child := range x {
			assertMessageFieldsSafe(t, child, path)
		}
	}
}

// assertNoControlBytes requires the raw response body to hold no control
// bytes other than the whitespace JSON allows between tokens. The route's
// recorder lets its cleanup detect a missing call or the wrong response body.
func assertNoControlBytes(t controlByteReporter, body []byte, checks *controlByteChecks) {
	t.Helper()
	checks.calls++
	checks.body = bytes.Clone(body)
	for i, c := range body {
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' {
			assert.Failf(t, "control byte in body", "byte 0x%02x at offset %d", c, i)
			return
		}
	}
}

func hostileGet(s *serverFixture, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, apiRequest(http.MethodGet, path, ""))
	return w
}

// TestHandlers_HostileUIDAndAnnotationID_MessagesAreSafeAndJSONExact drives
// every read route with a node whose uid and annotation id are pulled event
// ids: JSON stays exact, every message field is printable ASCII and no body
// holds a raw control byte.
func TestHandlers_HostileUIDAndAnnotationID_MessagesAreSafeAndJSONExact(t *testing.T) {
	s := testServer(t)
	seedHostileNode(t, s)
	paths := []string{
		"/api/v1/nodes/HS-1", "/api/v1/nodes/HS-1/children", "/api/v1/nodes/HS-1/ancestors",
		"/api/v1/nodes/HS-1/tree", "/api/v1/nodes/HS-1/activity", "/api/v1/context/HS-1",
		"/api/v1/progress/HS-1", "/api/v1/search?q=Synced", "/api/v1/ready", "/api/v1/stale",
		"/api/v1/orphans", "/api/v1/stats", "/api/v1/deps/HS-1",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			var controlChecks controlByteChecks
			w := hostileGet(s, p)
			t.Cleanup(func() {
				assert.Equal(t, 1, controlChecks.calls, "route %s must call assertNoControlBytes exactly once", p)
				assert.Equal(t, w.Body.Bytes(), controlChecks.body, "route %s must check its actual response body", p)
			})
			var body any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
			assertNoControlBytes(t, w.Body.Bytes(), &controlChecks)
			assertMessageFieldsSafe(t, body, "$")
		})
	}
	var node struct {
		UID         string `json:"uid"`
		Annotations []struct {
			ID string `json:"id"`
		} `json:"annotations"`
	}
	w := hostileGet(s, "/api/v1/nodes/HS-1")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &node))
	assert.Equal(t, hostileSyncID, node.UID)
	require.Len(t, node.Annotations, 1)
	assert.Equal(t, hostileAnnotationID, node.Annotations[0].ID)
}

// TestAdmin_Verify_HostileDuplicateUID_RecoveryTextIsPrintableASCII makes two
// nodes share a hostile uid and reads the real verify response.
func TestAdmin_Verify_HostileDuplicateUID_RecoveryTextIsPrintableASCII(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	applyHostileEvent(t, s.store, "evt-a", "HS-1", model.OpCreateNode, &model.CreateNodePayload{Title: "a"}, 1)
	applyHostileEvent(t, s.store, "evt-b", "HS-2", model.OpCreateNode, &model.CreateNodePayload{Title: "b"}, 2)
	_, err := s.store.WriteDB().ExecContext(ctx, `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = s.store.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = ?`, hostileSyncID)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, apiRequest(http.MethodPost, "/api/v1/admin/verify", ""))
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	recovery, _ := resp["uid_unique_recovery"].(string)
	require.NotEmpty(t, recovery)
	assert.True(t, isASCIIText(recovery), "%q", recovery)
	assert.Contains(t, recovery, `\x1b[31m`)
	assert.Contains(t, recovery, `\u00e9`)
	assert.Contains(t, recovery, `\u4e16`)
}
