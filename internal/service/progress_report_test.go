// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// progressTreeStore counts and optionally fails the subtree reads.
type progressTreeStore struct {
	store.Store
	treeCalls     int
	directReads   int
	treeErr       error
	afterTreeHook func()
}

func (s *progressTreeStore) GetTree(ctx context.Context, id string, depth int) ([]*model.Node, error) {
	s.treeCalls++
	if s.treeErr != nil {
		return nil, s.treeErr
	}
	nodes, err := s.Store.GetTree(ctx, id, depth)
	if s.afterTreeHook != nil {
		s.afterTreeHook()
	}
	return nodes, err
}

func (s *progressTreeStore) GetDirectChildren(ctx context.Context, id string) ([]*model.Node, error) {
	s.directReads++
	return s.Store.GetDirectChildren(ctx, id)
}

func progressChild(t *testing.T, svc *service.NodeService, parent, title string) string {
	t.Helper()
	n, err := svc.CreateNode(t.Context(), &service.CreateNodeRequest{
		Title: title, ParentID: parent, Project: "TEST", Creator: "tester"})
	require.NoError(t, err)
	return n.ID
}

func progressSetStatus(t *testing.T, svc *service.NodeService, id string, to model.Status) {
	t.Helper()
	require.NoError(t, svc.TransitionStatus(t.Context(), id, model.StatusInProgress, "", "tester"))
	require.NoError(t, svc.TransitionStatus(t.Context(), id, to, "", "tester"))
}

func progressRoot(t *testing.T, svc *service.NodeService) string {
	t.Helper()
	n, err := svc.CreateNode(t.Context(), &service.CreateNodeRequest{
		Title: "root", Project: "TEST", Creator: "tester"})
	require.NoError(t, err)
	return n.ID
}

// TestGetProgressReport_DoneLeafPlusInvalidatedGrandchild_FlagsDescendant
// covers FR-5.6a: an invalidated node below a direct child is counted.
func TestGetProgressReport_DoneLeafPlusInvalidatedGrandchild_FlagsDescendant(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	root := progressRoot(t, svc)
	done := progressChild(t, svc, root, "done leaf")
	other := progressChild(t, svc, root, "other")
	grand := progressChild(t, svc, other, "grandchild")
	progressSetStatus(t, svc, done, model.StatusDone)
	require.NoError(t, st.TransitionStatus(t.Context(), grand, model.StatusInvalidated, "r", "tester"))

	rep, err := svc.GetProgressReport(t.Context(), root)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.InvalidatedCount)
	assert.True(t, rep.HasInvalidatedDescendants)
	assert.False(t, rep.AllChildrenExcluded)
	assert.Equal(t, 1, rep.DoneCount)
	assert.Equal(t, 2, rep.TotalCount)
}

// TestGetProgressReport_AllChildrenCancelledOrInvalidated_FlagsAllExcluded
// covers FR-5.6b: a zero caused by exclusion says so.
func TestGetProgressReport_AllChildrenCancelledOrInvalidated_FlagsAllExcluded(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	root := progressRoot(t, svc)
	a := progressChild(t, svc, root, "cancelled")
	b := progressChild(t, svc, root, "invalidated")
	require.NoError(t, svc.TransitionStatus(t.Context(), a, model.StatusCancelled, "descoped", "tester"))
	require.NoError(t, st.TransitionStatus(t.Context(), b, model.StatusInvalidated, "r", "tester"))

	rep, err := svc.GetProgressReport(t.Context(), root)
	require.NoError(t, err)
	assert.True(t, rep.AllChildrenExcluded)
	assert.Equal(t, 0, rep.TotalCount)
	assert.Equal(t, 2, rep.ExcludedCount)
	assert.Equal(t, 1, rep.InvalidatedCount)
	assert.InDelta(t, 0.0, rep.Progress, 1e-9)
}

// TestGetProgressReport_LeafAndNoExclusions_FlagsClear verifies a childless node
// is not "all excluded".
func TestGetProgressReport_LeafAndNoExclusions_FlagsClear(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	root := progressRoot(t, svc)
	rep, err := svc.GetProgressReport(t.Context(), root)
	require.NoError(t, err)
	assert.False(t, rep.AllChildrenExcluded)
	assert.False(t, rep.HasInvalidatedDescendants)
	assert.Zero(t, rep.InvalidatedCount)
}

// TestGetProgressReport_ReadError_IsErrorNotZero verifies an enumeration
// failure is never reported as zero progress.
func TestGetProgressReport_ReadError_IsErrorNotZero(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	root := progressRoot(t, svc)
	progressChild(t, svc, root, "kid")
	boom := errors.New("injected subtree read failure")
	fault := &progressTreeStore{Store: st, treeErr: boom}
	faulty := service.NewNodeService(fault, &service.NoopBroadcaster{}, &service.StaticConfig{}, nil, fixedClock(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)))

	rep, err := faulty.GetProgressReport(t.Context(), root)
	require.Error(t, err)
	assert.Nil(t, rep)
	assert.ErrorIs(t, err, boom)
}

// TestGetProgressReport_OneSnapshot_SingleSubtreeRead verifies one store read
// serves the whole report, so a mutation between reads cannot split it.
func TestGetProgressReport_OneSnapshot_SingleSubtreeRead(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	root := progressRoot(t, svc)
	kid := progressChild(t, svc, root, "kid")
	spy := &progressTreeStore{Store: st}
	spy.afterTreeHook = func() { // a mutation landing right after the read
		_ = st.TransitionStatus(context.Background(), kid, model.StatusInvalidated, "late", "tester")
	}
	spySvc := service.NewNodeService(spy, &service.NoopBroadcaster{}, &service.StaticConfig{}, nil, fixedClock(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)))

	rep, err := spySvc.GetProgressReport(t.Context(), root)
	require.NoError(t, err)
	assert.Equal(t, 1, spy.treeCalls)
	assert.Zero(t, spy.directReads)
	assert.False(t, rep.HasInvalidatedDescendants, "report describes the single snapshot")
	assert.False(t, rep.AllChildrenExcluded)
	assert.Equal(t, 1, rep.TotalCount)
}
