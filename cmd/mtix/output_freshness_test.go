// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Freshness metadata preserves payload values and fresh response shapes.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONWriter_StaleResponse_PreservesPayload(t *testing.T) {
	for _, payload := range []string{`{"seq":9007199254740993,"nested":{"value":1}}`, `[1,2]`, `null`, `"text"`} {
		t.Run(payload, func(t *testing.T) {
			var output bytes.Buffer
			writer := &jsonWriter{w: &output, staleReason: "sync_lock_busy"}
			require.NoError(t, writer.WriteJSON(json.RawMessage(payload)))
			var result map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(output.Bytes(), &result))
			require.JSONEq(t, "true", string(result["stale"]))
			require.JSONEq(t, `"sync_lock_busy"`, string(result["stale_reason"]))
			if payload[0] == '{' {
				require.Equal(t, "9007199254740993", string(result["seq"]))
				require.JSONEq(t, `{"value":1}`, string(result["nested"]))
				require.NotContains(t, result, "data")
			} else {
				require.JSONEq(t, payload, string(result["data"]))
			}
		})
	}
}

func TestJSONWriter_FreshResponse_RetainsOriginalShape(t *testing.T) {
	for _, payload := range []string{`{"id":"TEST-1"}`, `[1,2]`, `null`} {
		t.Run(payload, func(t *testing.T) {
			var output bytes.Buffer
			require.NoError(t, (&jsonWriter{w: &output}).WriteJSON(json.RawMessage(payload)))
			require.JSONEq(t, payload, output.String())
		})
	}
}

type freshnessFailWriter struct{}

func (freshnessFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestJSONWriter_StaleResponse_PropagatesEncodingAndWriteErrors(t *testing.T) {
	var output bytes.Buffer
	writer := &jsonWriter{w: &output, staleReason: "sync_lock_busy"}
	require.Error(t, writer.WriteJSON(make(chan int)))
	require.Empty(t, output.String())
	writer.w = freshnessFailWriter{}
	require.ErrorIs(t, writer.WriteJSON(map[string]string{"id": "TEST-1"}), io.ErrClosedPipe)
}
