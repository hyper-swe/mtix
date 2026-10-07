// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

type mutationEvents struct{ events []service.Event }

func (b *mutationEvents) Broadcast(_ context.Context, event service.Event) error {
	b.events = append(b.events, event)
	return nil
}

func TestMCPWorkflowMutations_InvokeServicesAndBroadcast(t *testing.T) {
	cases := []struct {
		name  string
		event service.EventType
		args  string
	}{
		{"mtix_claim", service.EventNodeClaimed, `{"id":"PROJ-1","agent_id":"worker"}`},
		{"mtix_unclaim", service.EventNodeUnclaimed, `{"id":"PROJ-1","reason":"release"}`},
		{"mtix_cancel", service.EventNodeCancelled, `{"id":"PROJ-1","reason":"cancel"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			st := newInboxTestStore(t)
			inboxSeedNode(t, st, "PROJ-1")
			ctx := context.Background()
			if tt.name == "mtix_unclaim" {
				require.NoError(t, st.ClaimNode(ctx, "PROJ-1", "worker"))
			}
			bus := &mutationEvents{}
			nodes := service.NewNodeService(st, bus, nil, nil, fixedClock)
			reg := NewToolRegistry()
			RegisterWorkflowTools(reg, nodes, service.NewBackgroundService(st, nil, nil, fixedClock))
			result, err := reg.Call(ctx, tt.name, json.RawMessage(tt.args))
			require.NoError(t, err)
			require.False(t, result.IsError)
			require.Len(t, bus.events, 1, "tool must invoke the owning service")
			require.Equal(t, tt.event, bus.events[0].Type)
			require.Equal(t, "PROJ-1", bus.events[0].NodeID)
		})
	}
}

func TestMCPDependencyMutations_InvokeServicesAndBroadcast(t *testing.T) {
	for _, name := range []string{"mtix_dep_add", "mtix_dep_remove"} {
		t.Run(name, func(t *testing.T) {
			st := newInboxTestStore(t)
			ctx := context.Background()
			inboxSeedNode(t, st, "PROJ-1")
			inboxSeedNode(t, st, "PROJ-2")
			dep := &model.Dependency{FromID: "PROJ-1", ToID: "PROJ-2", DepType: model.DepTypeRelated}
			if name == "mtix_dep_remove" {
				require.NoError(t, st.AddDependency(ctx, dep))
			}
			bus := &mutationEvents{}
			svc := service.NewDependencyService(st, bus, nil, fixedClock)
			reg := NewToolRegistry()
			RegisterDepTools(reg, svc)
			result, err := reg.Call(ctx, name, json.RawMessage(`{"from_id":"PROJ-1","to_id":"PROJ-2","dep_type":"related"}`))
			require.NoError(t, err)
			require.False(t, result.IsError)
			require.Len(t, bus.events, 1, "tool must invoke the owning service")
			want := service.EventDependencyAdded
			if name == "mtix_dep_remove" {
				want = service.EventDependencyRemoved
			}
			require.Equal(t, want, bus.events[0].Type)
			require.Equal(t, "PROJ-1", bus.events[0].NodeID)
		})
	}
}

type ackServiceSpy struct {
	agent string
	seq   int64
	calls int
}

func (s *ackServiceSpy) InboxAck(_ context.Context, agent string, seq int64) error {
	s.agent = agent
	s.seq = seq
	s.calls++
	return nil
}

type ackBackend struct{ InboxStore }

func (s *ackBackend) InboxAck(context.Context, string, int64) error { return nil }

func TestMCPInboxAck_InvokesService(t *testing.T) {
	st := newInboxTestStore(t)
	svc := &ackServiceSpy{}
	reg := NewToolRegistry()
	RegisterInboxTools(reg, testInboxService(&ackBackend{st}, svc))
	result, err := reg.Call(context.Background(), "mtix_inbox_ack", json.RawMessage(`{"agent":"worker","seq":7}`))
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, 1, svc.calls, "ack mutation must invoke the owning service")
	require.Equal(t, "worker", svc.agent)
	require.Equal(t, int64(7), svc.seq)
}

func TestMCPMutations_FailedWritesPreserveErrorWithoutBroadcast(t *testing.T) {
	cases := []struct{ name, args string }{
		{"mtix_claim", `{"id":"MISSING-1","agent_id":"worker"}`},
		{"mtix_unclaim", `{"id":"MISSING-1","reason":"release"}`},
		{"mtix_cancel", `{"id":"MISSING-1","reason":"cancel"}`},
		{"mtix_dep_add", `{"from_id":"MISSING-1","to_id":"MISSING-2","dep_type":"related"}`},
		{"mtix_dep_remove", `{"from_id":"MISSING-1","to_id":"MISSING-2","dep_type":"related"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			st := newInboxTestStore(t)
			bus := &mutationEvents{}
			nodes := service.NewNodeService(st, bus, nil, nil, fixedClock)
			reg := NewToolRegistry()
			RegisterWorkflowTools(reg, nodes, service.NewBackgroundService(st, nil, nil, fixedClock))
			RegisterDepTools(reg, service.NewDependencyServiceFromNodeService(nodes))
			_, err := reg.Call(context.Background(), tt.name, json.RawMessage(tt.args))
			if tt.name == "mtix_dep_add" {
				require.ErrorContains(t, err, "FOREIGN KEY constraint failed")
			} else {
				require.ErrorIs(t, err, model.ErrNotFound)
			}
			require.Empty(t, bus.events)
		})
	}
}

type failingAckService struct {
	err   error
	calls int
}

func (s *failingAckService) InboxAck(context.Context, string, int64) error { s.calls++; return s.err }

func TestMCPInboxAck_ServiceErrorIsPreserved(t *testing.T) {
	st := newInboxTestStore(t)
	sentinel := errors.New("service refused ack")
	svc := &failingAckService{err: sentinel}
	reg := NewToolRegistry()
	RegisterInboxTools(reg, testInboxService(st, svc))
	_, err := reg.Call(context.Background(), "mtix_inbox_ack", json.RawMessage(`{"agent":"worker","seq":7}`))
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 1, svc.calls)
}

type errorAckBackend struct {
	InboxStore
	err error
}

func (b *errorAckBackend) InboxAck(context.Context, string, int64) error { return b.err }

func TestMCPServiceBoundary_PreservesExactBackendMessages(t *testing.T) {
	st := newInboxTestStore(t)
	ctx := context.Background()
	nodes := service.NewNodeService(st, nil, nil, nil, fixedClock)
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, nodes, service.NewBackgroundService(st, nil, nil, fixedClock))
	backend := st.CancelNode(ctx, "MISSING-1", "cancel", "", false)
	_, err := reg.Call(ctx, "mtix_cancel", json.RawMessage(`{"id":"MISSING-1","reason":"cancel"}`))
	require.ErrorIs(t, err, model.ErrNotFound)
	require.Equal(t, backend.Error(), err.Error())
}

func TestMCPInboxBoundary_PreservesExactBackendMessage(t *testing.T) {
	st := newInboxTestStore(t)
	ctx := context.Background()
	reg := NewToolRegistry()
	ackError := errors.New("original inbox backend context: durable journal unavailable")
	ack := &errorAckBackend{InboxStore: st, err: ackError}
	RegisterInboxTools(reg, testInboxService(ack, service.NewInboxService(ack)))
	_, err := reg.Call(ctx, "mtix_inbox_ack", json.RawMessage(`{"agent":"worker","seq":7}`))
	require.ErrorIs(t, err, ackError)
	require.Equal(t, ackError.Error(), err.Error())
}
