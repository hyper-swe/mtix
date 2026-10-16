// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reviewR1PublicationLookupStore struct {
	service.PermissionStore
	calls   int
	failure error
}

func (s *reviewR1PublicationLookupStore) PermissionPublicationExists(context.Context, string) (bool, error) {
	s.calls++
	if s.calls == 1 {
		return false, nil
	}
	return true, s.failure
}

func TestReviewR1_PublicationIdentityLookupRefusesBeforeAcceptingGrants(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "collisions", true: "unreadable"}[fail], func(t *testing.T) {
			f := newWakePermissionFixture(t)
			permissionRoot(t, f, "TEST-1")
			fault := &reviewR1PublicationLookupStore{PermissionStore: f.store}
			sentinel := errors.New("synthetic publication lookup unavailable")
			if fail {
				fault.failure = sentinel
			}
			f.svc.Store = fault
			err := f.svc.ApplyBatch(t.Context(), f.board, filePermissionReplayBatch())
			if fail {
				require.ErrorIs(t, err, sentinel)
				require.Equal(t, 2, fault.calls)
			} else {
				require.ErrorIs(t, err, model.ErrConflict)
				require.Equal(t, 5, fault.calls)
			}
			grants, err := f.state.AgentGrants()
			require.NoError(t, err)
			require.Empty(t, grants)
			rows, err := f.state.Confirmations(t.Context())
			require.NoError(t, err)
			require.Empty(t, rows)
			node, err := f.store.GetNode(t.Context(), "TEST-1")
			require.NoError(t, err)
			require.Empty(t, node.Annotations)
		})
	}
}

type reviewR1NoticeReadStore struct {
	service.PermissionStore
	failure        error
	deleteAtAppend bool
}

func (s *reviewR1NoticeReadStore) GetNode(ctx context.Context, id string) (*model.Node, error) {
	if s.failure != nil {
		return nil, s.failure
	}
	return s.PermissionStore.GetNode(ctx, id)
}

func (s *reviewR1NoticeReadStore) AppendAnnotationOnce(ctx context.Context, id string, annotation model.Annotation) error {
	if s.deleteAtAppend {
		if err := s.DeleteNode(ctx, id, false, "synthetic notice race"); err != nil {
			return err
		}
	}
	return s.PermissionStore.AppendAnnotationOnce(ctx, id, annotation)
}

func TestReviewR1_UnreadableNoticeTargetHoldsAllowUntilStableRetry(t *testing.T) {
	f := newWakePermissionFixture(t)
	reviewR1Hub(t, f, "first")
	sentinel := errors.New("synthetic target unreadable")
	f.svc.Store = &reviewR1NoticeReadStore{PermissionStore: f.store, failure: sentinel}
	batch := filePermissionReplayBatch()
	require.ErrorIs(t, f.svc.ApplyBatch(t.Context(), f.board, batch), sentinel)
	publication := permissionAcceptedPublicationID(t, f, batch.ID)
	ok, _, err := f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	require.False(t, ok)
	f.svc.Store = f.store
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	assert.Equal(t, publication, node.Annotations[0].ID)
	assert.Equal(t, 1, permissionWireEventCount(t, f.store, publication))
}

func TestReviewR1_TargetDeletedBetweenProbeAndAppendIsUndeliverable(t *testing.T) {
	f := newWakePermissionFixture(t)
	reviewR1Hub(t, f, "first")
	f.svc.Store = &reviewR1NoticeReadStore{PermissionStore: f.store, deleteAtAppend: true}
	batch := filePermissionReplayBatch()
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "undeliverable", rows[0].BoardLine)
	assert.False(t, rows[0].Pending)
	result, available, err := f.state.PermissionReplayResult(t.Context(), f.board.ID, batch.ID)
	require.NoError(t, err)
	require.True(t, available)
	exists, err := f.store.PermissionPublicationExists(t.Context(), result.PublicationID)
	require.NoError(t, err)
	assert.False(t, exists, "a removed target receives no comment event")
}
