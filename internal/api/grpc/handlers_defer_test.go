// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hyper-swe/mtix/internal/model"
)

// deferAuthors returns the author of the node's last activity entry and of
// its transition_status sync event.
func deferAuthors(t *testing.T, s *Server, id string) (activity, event string) {
	t.Helper()
	ctx := context.Background()
	entries, err := s.store.GetActivity(ctx, id, 100, 0)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	require.NoError(t, s.store.QueryRow(ctx,
		`SELECT author_id FROM sync_events WHERE node_id = ? AND op_type = ?`,
		id, string(model.OpTransitionStatus)).Scan(&event))
	return entries[len(entries)-1].Author, event
}

// TestDefer_WithUntil_StoresDeferUntil verifies the Defer RPC passes
// DeferRequest.until through and stores it as UTC RFC 3339, that a request
// without until stores no wake time, and that the request's agent is the
// author of the activity entry and the sync event (MTIX-95.22, FR-3.8b).
func TestDefer_WithUntil_StoresDeferUntil(t *testing.T) {
	offset := time.FixedZone("UTC+9", 9*60*60)
	until := time.Date(2031, 1, 1, 9, 0, 0, 0, offset)

	tests := []struct {
		name      string
		until     *time.Time
		wantValid bool
		want      string
	}{
		{"until in another zone is stored in utc", &until, true, "2031-01-01T00:00:00Z"},
		{"no until stores no wake time", nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testGRPCServer(t)
			ctx := context.Background()
			created := createTestNode(t, s, "Defer Until", "DEFER")

			node, err := s.HandleDefer(ctx, created.ID, "agent-1", tt.until)
			require.NoError(t, err)
			assert.Equal(t, model.StatusDeferred, node.Status)

			var got sql.NullString
			require.NoError(t, s.store.QueryRow(ctx,
				`SELECT defer_until FROM nodes WHERE id = ?`, created.ID).Scan(&got))
			assert.Equal(t, tt.wantValid, got.Valid)
			assert.Equal(t, tt.want, got.String)
			if tt.wantValid {
				require.NotNil(t, node.DeferUntil)
				assert.True(t, node.DeferUntil.Equal(until), "returned node carries the wake time")
			}
			activity, event := deferAuthors(t, s, created.ID)
			assert.Equal(t, "agent-1", activity, "activity author is the request's agent")
			assert.Equal(t, "agent-1", event, "sync event author is the request's agent")
		})
	}
}

// TestHandleDefer_UntilOutsideStorableYears_InvalidArgument verifies the
// Defer RPC, which receives a time.Time and runs no text parser, rejects a
// wake time whose UTC year is outside 1..9999 with InvalidArgument and leaves
// the node readable and open; the last storable second is accepted
// (MTIX-95.22 round 3).
func TestHandleDefer_UntilOutsideStorableYears_InvalidArgument(t *testing.T) {
	west := time.FixedZone("UTC-5", -5*60*60)
	tests := []struct {
		name  string
		until time.Time
		want  string // stored text; "" means rejected
	}{
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), ""},
		{"year -1", time.Date(-1, 6, 1, 0, 0, 0, 0, time.UTC), ""},
		{"one second past year 9999, offset form", time.Date(9999, 12, 31, 19, 0, 0, 0, west), ""},
		{"last storable second, offset form", time.Date(9999, 12, 31, 18, 59, 59, 0, west), "9999-12-31T23:59:59Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testGRPCServer(t)
			ctx := context.Background()
			created := createTestNode(t, s, "Defer Edge", "DEFER")

			_, err := s.HandleDefer(ctx, created.ID, "agent-1", &tt.until)

			node, getErr := s.HandleGetNode(ctx, created.ID)
			require.NoError(t, getErr, "the node stays readable")
			if tt.want == "" {
				st, ok := status.FromError(err)
				require.True(t, ok)
				assert.Equal(t, codes.InvalidArgument, st.Code())
				assert.Equal(t, model.StatusOpen, node.Status)
				assert.Nil(t, node.DeferUntil)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, node.DeferUntil)
			assert.Equal(t, tt.want, node.DeferUntil.UTC().Format(time.RFC3339))
		})
	}
}
