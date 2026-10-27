// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCommentHop_OptionalNonnegativeInteger_RejectsInvalidWireValues(t *testing.T) {
	for _, raw := range []string{`{"hop":null}`, `{"hop":-1}`, `{"hop":1.5}`, `{"hop":"2"}`, `{"hop":9223372036854775808}`} {
		t.Run(raw, func(t *testing.T) {
			var payload model.CommentPayload
			require.Error(t, json.Unmarshal([]byte(raw), &payload))
		})
	}
	for _, raw := range []string{`{}`, `{"hop":0}`, `{"hop":32}`} {
		t.Run(raw, func(t *testing.T) {
			var payload model.CommentPayload
			require.NoError(t, json.Unmarshal([]byte(raw), &payload))
		})
	}
}

func TestCommentHop_Encoding_RefusesNegativeAndOmitsZero(t *testing.T) {
	_, err := json.Marshal(model.CommentPayload{To: "worker", Hop: -1})
	require.Error(t, err)
	raw, err := json.Marshal(model.CommentPayload{To: "worker"})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "hop")
	raw, err = json.Marshal(model.CommentPayload{To: "worker", Hop: 32})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"hop":32`)
}
