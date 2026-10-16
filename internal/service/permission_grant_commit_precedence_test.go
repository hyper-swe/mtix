// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reviewR1NoticeStore struct {
	service.PermissionStore
	blocked string
}

func (s *reviewR1NoticeStore) AppendAnnotationOnce(ctx context.Context, node string, annotation model.Annotation) error {
	if s.blocked == "" {
		s.blocked = annotation.ID
	}
	if annotation.ID == s.blocked {
		return errors.New("synthetic unavailable earlier publication")
	}
	return s.PermissionStore.AppendAnnotationOnce(ctx, node, annotation)
}

func reviewR1Hub(t *testing.T, f *wakePermissionFixture, agent string) {
	t.Helper()
	permissionRoot(t, f, "TEST-1")
	f.register(t, agent)
	_, err := f.store.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
}

func reviewR1Apply(t *testing.T, f *wakePermissionFixture, agent, action string) error {
	t.Helper()
	return f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "yes", Source: "review operator", Changes: []oplocal.PermissionChange{{AgentID: agent, Action: action}}})
}

func reviewR1Denied(t *testing.T, f *wakePermissionFixture, agent string) {
	t.Helper()
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, agent)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "denied", reason)
}

func TestWakeable_PendingAllow_RequiresCommit(t *testing.T) {
	f := newWakePermissionFixture(t)
	reviewR1Hub(t, f, "pending")
	f.svc.Store = &reviewR1NoticeStore{PermissionStore: f.store}
	require.Error(t, reviewR1Apply(t, f, "pending", "allow"))
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "pending")
	require.NoError(t, err)
	assert.False(t, ok, "an unfinished allow must not become effective")
	assert.NotEqual(t, "confirmed", reason)
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].Pending)
}

func TestApplyBatch_DeletedNoticeTarget_DoesNotBlockDeny(t *testing.T) {
	f := newWakePermissionFixture(t)
	reviewR1Hub(t, f, "first")
	f.svc.Store = &reviewR1NoticeStore{PermissionStore: f.store}
	require.Error(t, reviewR1Apply(t, f, "first", "allow"))
	f.svc.Store = f.store
	require.NoError(t, f.store.DeleteNode(t.Context(), "TEST-1", false, "review fixture"))
	assert.NoError(t, reviewR1Apply(t, f, "first", "deny"))
	reviewR1Denied(t, f, "first")
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	undeliverable := false
	for _, row := range rows {
		undeliverable = undeliverable || row.Action == "allow" && row.BoardLine == "undeliverable"
	}
	assert.True(t, undeliverable, "retain the actual prior notice outcome")
	assert.GreaterOrEqual(t, len(rows), 2, "retain both operator decisions")
}

func TestApplyBatch_EarlierPublicationFailure_DoesNotBlockDeny(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_board", true: "other_board"}[foreign], func(t *testing.T) {
			owner := newWakePermissionFixture(t)
			reviewR1Hub(t, owner, "pending")
			target := owner
			if foreign {
				target = newWakePermissionFixture(t)
				target.board.ID = "22222222-2222-4222-8222-222222222222"
				target.state, target.svc.State, target.svc.HostID = owner.state, owner.state, owner.svc.HostID
				reviewR1Hub(t, target, "existing")
			} else {
				target.register(t, "existing")
			}
			require.NoError(t, reviewR1Apply(t, target, "existing", "allow"))
			owner.svc.Store = &reviewR1NoticeStore{PermissionStore: owner.store}
			require.Error(t, reviewR1Apply(t, owner, "pending", "allow"))
			ok, _, err := target.svc.Wakeable(t.Context(), target.board, "existing")
			require.NoError(t, err)
			require.True(t, ok, "the earlier committed allow is the deny target")
			assert.NoError(t, reviewR1Apply(t, target, "existing", "deny"))
			reviewR1Denied(t, target, "existing")
		})
	}
}
