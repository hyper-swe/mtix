// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestAppendAnnotation_HopOnAddressedAndUnaddressed_StoresHopZeroWhenUnaddressed
// pins the store hop-0 rule (FR-20.3): a comment with no addressee stores hop 0
// even when the caller supplies a wake-context hop, while an addressed comment
// keeps the hop it was given.
func TestAppendAnnotation_HopOnAddressedAndUnaddressed_StoresHopZeroWhenUnaddressed(t *testing.T) {
	tests := []struct {
		name      string
		addressee string
		hop       int64
		wantHop   int64
	}{
		{"unaddressed with wake hop stores zero", "", 5, 0},
		{"unaddressed with zero hop stores zero", "", 0, 0},
		{"addressed keeps given hop", "worker", 5, 5},
		{"addressed zero hop stays zero", "worker", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			require.NoError(t, s.AppendAnnotation(t.Context(), "MTIX-1", model.Annotation{
				ID: "c1", Author: "sender", Text: "body", Addressee: tt.addressee, Hop: tt.hop,
				CreatedAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
			}))
			var raw string
			require.NoError(t, s.WriteDB().QueryRowContext(t.Context(),
				// Latest comment event payload for the node, as stored for sync.
				`SELECT payload FROM sync_events WHERE node_id = ? AND op_type = ? ORDER BY rowid DESC LIMIT 1`,
				"MTIX-1", string(model.OpComment)).Scan(&raw))
			var payload model.CommentPayload
			require.NoError(t, json.Unmarshal([]byte(raw), &payload))
			require.Equal(t, tt.wantHop, payload.Hop)
		})
	}
}
