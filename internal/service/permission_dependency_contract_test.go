// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

type permissionReadFailure struct {
	service.PermissionStore
	registration, addressed, publication, nodes, hub error
	addressedIDs                                     []string
}

func (s permissionReadFailure) ListRegistrations(ctx context.Context, host, uid string) ([]sqlite.Registration, error) {
	if s.registration != nil {
		return nil, s.registration
	}
	return s.PermissionStore.ListRegistrations(ctx, host, uid)
}

func (s permissionReadFailure) PermissionAddressedAgents(ctx context.Context) ([]string, error) {
	if s.addressed != nil || s.addressedIDs != nil {
		return s.addressedIDs, s.addressed
	}
	return s.PermissionStore.PermissionAddressedAgents(ctx)
}

func (s permissionReadFailure) PermissionPublicationExists(ctx context.Context, id string) (bool, error) {
	if s.publication != nil {
		return false, s.publication
	}
	return s.PermissionStore.PermissionPublicationExists(ctx, id)
}

func (s permissionReadFailure) ListNodes(ctx context.Context, filter store.NodeFilter, opts store.ListOptions) ([]*model.Node, int, error) {
	if s.nodes != nil {
		return nil, 0, s.nodes
	}
	return s.PermissionStore.ListNodes(ctx, filter, opts)
}

func (s permissionReadFailure) HubMark(ctx context.Context) (sqlite.HubMark, error) {
	if s.hub != nil {
		return sqlite.HubMark{}, s.hub
	}
	return s.PermissionStore.HubMark(ctx)
}

func requirePermissionFixtureUnchanged(t *testing.T, f *wakePermissionFixture) {
	t.Helper()
	grants, err := f.state.AgentGrants()
	require.NoError(t, err)
	require.Empty(t, grants)
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Empty(t, node.Annotations)
	var count int
	require.NoError(t, f.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM sync_events WHERE op_type = ?`, model.OpComment).Scan(&count))
	require.Zero(t, count)
}

func TestApplyBatch_DependencyReadFailureNeverAcceptsPartialPermissions(t *testing.T) {
	for _, stage := range []string{"registration", "publication", "nodes"} {
		t.Run(stage, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			permissionRoot(t, f, "TEST-1")
			cause := errors.New("synthetic permission dependency failure")
			fault := permissionReadFailure{PermissionStore: f.store}
			switch stage {
			case "registration":
				fault.registration = cause
			case "publication":
				fault.publication = cause
			case "nodes":
				fault.nodes = cause
			}
			f.svc.Store = fault
			require.ErrorIs(t, f.svc.ApplyBatch(t.Context(), f.board, filePermissionReplayBatch()), cause)
			requirePermissionFixtureUnchanged(t, f)
		})
	}
}

func TestCandidates_DependencyFailuresNeverReturnPartialFacts(t *testing.T) {
	for _, stage := range []string{"registration", "addressed", "invalid_address"} {
		t.Run(stage, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.register(t, "first")
			permissionRoot(t, f, "TEST-1")
			fault := permissionReadFailure{PermissionStore: f.store}
			cause := errors.New("synthetic candidate dependency failure")
			switch stage {
			case "registration":
				fault.registration = cause
			case "addressed":
				fault.addressed = cause
			case "invalid_address":
				fault.addressedIDs = []string{strings.Repeat("x", 65)}
			}
			f.svc.Store = fault
			facts, err := f.svc.Candidates(t.Context(), f.board)
			require.Error(t, err)
			require.Nil(t, facts)
			requirePermissionFixtureUnchanged(t, f)
		})
	}
}
