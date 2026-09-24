// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestParseDeferUntil_Inputs_ReturnUTCOrInvalidInput verifies the boundary
// parser for a defer wake time: empty means none, RFC 3339 is returned in
// UTC, and anything else is ErrInvalidInput (MTIX-95.22, FR-3.8b).
func TestParseDeferUntil_Inputs_ReturnUTCOrInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string // RFC 3339 UTC; "" means nil
		wantErr bool
	}{
		{"empty means no wake time", "", "", false},
		{"utc", "2031-01-01T00:00:00Z", "2031-01-01T00:00:00Z", false},
		{"offset converted to utc", "2031-01-01T01:00:00+01:00", "2031-01-01T00:00:00Z", false},
		{"fractional seconds kept by the parser", "2031-01-01T00:00:00.5Z", "2031-01-01T00:00:00.5Z", false},
		{"free text", "tomorrow", "", true},
		{"date only", "2031-01-01", "", true},
		{"no zone", "2031-01-01T00:00:00", "", true},
		{"surrounding space", " 2031-01-01T00:00:00Z", "", true},
		// MTIX-95.22 round 3: the UTC year must be 1..9999, or the stored
		// RFC 3339 text cannot be read back.
		{"last storable second, offset form", "9999-12-31T18:59:59-05:00", "9999-12-31T23:59:59Z", false},
		{"one second past year 9999, offset form", "9999-12-31T19:00:00-05:00", "", true},
		{"year 10000 in utc, offset form", "9999-12-31T23:00:00-05:00", "", true},
		{"first storable second, offset form", "0001-01-01T01:00:00+01:00", "0001-01-01T00:00:00Z", false},
		{"one second before year 1, offset form", "0001-01-01T00:59:59+01:00", "", true},
		{"year -1 in utc, offset form", "0000-01-01T00:30:00+01:00", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.ParseDeferUntil(tt.raw)
			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tt.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, time.UTC, got.Location())
			assert.Equal(t, tt.want, got.Format(time.RFC3339Nano))
		})
	}
}

// TestNodeService_DeferNode_StoresWakeTimeAndBroadcasts verifies the service
// defers through the store with the wake time in UTC, truncated to whole
// seconds, and broadcasts a status change by the caller (MTIX-95.22).
func TestNodeService_DeferNode_StoresWakeTimeAndBroadcasts(t *testing.T) {
	svc, s, bc := newTestNodeService(t)
	ctx := context.Background()
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Defer", Creator: "admin",
	})
	require.NoError(t, err)
	bc.Reset()
	until := time.Date(2031, 1, 1, 3, 0, 0, 900_000_000, time.FixedZone("UTC+3", 3*60*60))

	require.NoError(t, svc.DeferNode(ctx, node.ID, &until, "waiting", "agent-7"))

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDeferred, got.Status)
	require.NotNil(t, got.DeferUntil)
	assert.Equal(t, "2031-01-01T00:00:00Z", got.DeferUntil.UTC().Format(time.RFC3339Nano))
	events := bc.Events()
	require.Len(t, events, 1)
	assert.Equal(t, service.EventStatusChanged, events[0].Type)
	assert.Equal(t, "agent-7", events[0].Author)
}

// TestNodeService_DeferNode_InvalidTransition_ReturnsError verifies the
// service rejects a defer the state machine forbids, without storing a wake
// time or broadcasting (MTIX-95.22).
func TestNodeService_DeferNode_InvalidTransition_ReturnsError(t *testing.T) {
	svc, s, bc := newTestNodeService(t)
	ctx := context.Background()
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Defer", Creator: "admin",
	})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusCancelled, "dropped", "admin"))
	bc.Reset()
	until := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

	err = svc.DeferNode(ctx, node.ID, &until, "waiting", "agent-7")
	require.ErrorIs(t, err, model.ErrInvalidTransition)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got.Status)
	assert.Nil(t, got.DeferUntil)
	assert.Empty(t, bc.Events())
}

// TestNodeService_DeferNode_MissingNode_ReturnsNotFound verifies a defer of a
// node that does not exist returns ErrNotFound (MTIX-95.22).
func TestNodeService_DeferNode_MissingNode_ReturnsNotFound(t *testing.T) {
	svc, _, _ := newTestNodeService(t)

	err := svc.DeferNode(context.Background(), "PROJ-404", nil, "waiting", "agent-7")
	require.ErrorIs(t, err, model.ErrNotFound)
}

// TestNodeService_DeferNode_WithoutUntil_ClearsWakeTime verifies a defer with
// no wake time stores none, replacing one set by an earlier defer
// (MTIX-95.22).
func TestNodeService_DeferNode_WithoutUntil_ClearsWakeTime(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Defer", Creator: "admin",
	})
	require.NoError(t, err)
	until := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, svc.DeferNode(ctx, node.ID, &until, "waiting", "agent-7"))

	require.NoError(t, svc.DeferNode(ctx, node.ID, nil, "waiting indefinitely", "agent-7"))

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDeferred, got.Status)
	assert.Nil(t, got.DeferUntil)
}

// TestNodeService_DeferNode_UntilOutsideStorableYears_ReturnsInvalidInput
// verifies the service rejects a wake time whose UTC year is outside 1..9999
// even when no parser ran (the gRPC path passes a time.Time), and leaves the
// node readable and unchanged; the last and first storable seconds are
// accepted (MTIX-95.22 round 3).
func TestNodeService_DeferNode_UntilOutsideStorableYears_ReturnsInvalidInput(t *testing.T) {
	east := time.FixedZone("UTC+1", 60*60)
	west := time.FixedZone("UTC-5", -5*60*60)
	tests := []struct {
		name    string
		until   time.Time
		want    string // stored UTC text; "" means rejected
		wantErr bool
	}{
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), "", true},
		{"year 9999 west of utc crossing into 10000", time.Date(9999, 12, 31, 19, 0, 0, 0, west), "", true},
		{"year -1", time.Date(-1, 12, 31, 0, 0, 0, 0, time.UTC), "", true},
		{"year 1 east of utc crossing into year 0", time.Date(1, 1, 1, 0, 59, 59, 0, east), "", true},
		{"last storable second", time.Date(9999, 12, 31, 18, 59, 59, 0, west), "9999-12-31T23:59:59Z", false},
		{"first storable second", time.Date(1, 1, 1, 1, 0, 0, 0, east), "0001-01-01T00:00:00Z", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, s, _ := newTestNodeService(t)
			ctx := context.Background()
			node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
				Project: "PROJ", Title: "Defer", Creator: "admin",
			})
			require.NoError(t, err)

			err = svc.DeferNode(ctx, node.ID, &tt.until, "waiting", "agent-7")

			got, getErr := s.GetNode(ctx, node.ID)
			require.NoError(t, getErr, "the node stays readable")
			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				assert.Equal(t, model.StatusOpen, got.Status)
				assert.Nil(t, got.DeferUntil)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got.DeferUntil)
			assert.Equal(t, tt.want, got.DeferUntil.UTC().Format(time.RFC3339))
		})
	}
}

// TestNodeService_DeferNode_UnstorableUntil_RejectedBeforeTheStore verifies
// the service checks the wake time's UTC year itself, before any store call,
// rather than relying on the store to refuse it (MTIX-95.22 round 3). The
// mock store would answer ErrNotFound to any read.
func TestNodeService_DeferNode_UnstorableUntil_RejectedBeforeTheStore(t *testing.T) {
	svc := service.NewNodeService(&mockStore{}, nil, nil, nil, fixedClock(time.Now()))
	until := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)

	err := svc.DeferNode(context.Background(), "PROJ-1", &until, "waiting", "agent-7")

	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.NotErrorIs(t, err, model.ErrNotFound)
}
