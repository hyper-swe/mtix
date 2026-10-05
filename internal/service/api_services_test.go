// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// API service tests verify persistence and successful-only broadcasts.
func TestAPIWorkflowServices_PersistAndBroadcast(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name  string
		event service.EventType
	}{
		{"claim", service.EventNodeClaimed}, {"unclaim", service.EventNodeUnclaimed}, {"cancel", service.EventNodeCancelled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, st, bc := newTestNodeService(t)
			n, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Title: "workflow", Project: "TEST"})
			require.NoError(t, err)
			if tt.name == "unclaim" {
				require.NoError(t, st.ClaimNode(ctx, n.ID, "writer"))
			}
			bc.events = nil
			switch tt.name {
			case "claim":
				err = svc.ClaimNode(ctx, n.ID, "writer")
			case "unclaim":
				err = svc.UnclaimNode(ctx, n.ID, "release", "writer")
			case "cancel":
				err = svc.CancelNode(ctx, n.ID, "cancel", "writer", false)
			}
			require.NoError(t, err)
			require.Len(t, bc.events, 1)
			require.Equal(t, tt.event, bc.events[0].Type)
			bc.events = nil
			err = svc.ClaimNode(ctx, "TEST-999", "writer")
			require.ErrorIs(t, err, model.ErrNotFound)
			require.Empty(t, bc.events)
		})
	}
}
func TestAPIDependencyService_PersistAndBroadcast(t *testing.T) {
	svc, st, bc := newTestNodeService(t)
	ctx := context.Background()
	a, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Title: "a", Project: "TEST"})
	require.NoError(t, err)
	b, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Title: "b", Project: "TEST"})
	require.NoError(t, err)
	depSvc := service.NewDependencyService(st, bc, nil, func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) })
	dep := &model.Dependency{FromID: a.ID, ToID: b.ID, DepType: model.DepTypeRelated, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	bc.events = nil
	require.NoError(t, depSvc.AddDependency(ctx, dep))
	require.Len(t, bc.events, 1)
	require.Equal(t, service.EventDependencyAdded, bc.events[0].Type)
	require.NoError(t, depSvc.RemoveDependency(ctx, a.ID, b.ID, model.DepTypeRelated))
	require.Len(t, bc.events, 2)
	require.Equal(t, service.EventDependencyRemoved, bc.events[1].Type)
	bc.events = nil
	require.ErrorIs(t, depSvc.RemoveDependency(ctx, a.ID, b.ID, model.DepTypeRelated), model.ErrNotFound)
	require.Empty(t, bc.events)
}

func TestAPIAdminService_VerifyBackupAndClose(t *testing.T) {
	_, st, _ := newTestNodeService(t)
	ctx := context.Background()
	admin := service.NewAdminService(st)
	report, err := admin.Verify(ctx)
	require.NoError(t, err)
	require.Equal(t, "ok", report.Status)
	require.Empty(t, report.UIDReport)
	backup, err := admin.Backup(ctx, filepath.Join(t.TempDir(), "verified.db"))
	require.NoError(t, err)
	require.True(t, backup.Verified)
	require.Greater(t, backup.Size, int64(0))
	require.NoError(t, admin.Close())
	_, err = admin.Verify(ctx)
	require.Error(t, err)
}

// Query DTO conversions and read errors keep the service boundary observable.
func TestAPINodeQueries_ResultsAndBackendErrors(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	ctx := context.Background()
	n, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Title: "query", Project: "TEST"})
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		query func() error
	}{
		{"activity", func() error { _, e := svc.GetActivity(ctx, n.ID, 10, 0); return e }},
		{"children", func() error { _, e := svc.GetDirectChildren(ctx, n.ID); return e }},
		{"ancestors", func() error { _, e := svc.GetAncestorChain(ctx, n.ID); return e }},
		{"siblings", func() error { _, e := svc.GetSiblings(ctx, n.ID); return e }},
		{"projects", func() error { _, e := svc.DistinctProjects(ctx); return e }},
		{"list", func() error {
			_, _, e := svc.ListNodes(ctx, service.NodeFilter{Project: "TEST"}, service.ListOptions{Limit: 10})
			return e
		}},
	} {
		t.Run(test.name, func(t *testing.T) { require.NoError(t, test.query()) })
	}
	title := "updated through service DTO"
	require.NoError(t, svc.ApplyUpdate(ctx, n.ID, &service.NodeUpdate{Title: &title}))
	got, err := svc.GetNode(ctx, n.ID)
	require.NoError(t, err)
	require.Equal(t, title, got.Title)
	require.NoError(t, st.Close())
	for _, test := range []struct {
		name  string
		query func() error
	}{
		{"activity", func() error { _, e := svc.GetActivity(ctx, n.ID, 10, 0); return e }},
		{"children", func() error { _, e := svc.GetDirectChildren(ctx, n.ID); return e }},
		{"ancestors", func() error { _, e := svc.GetAncestorChain(ctx, n.ID); return e }},
		{"siblings", func() error { _, e := svc.GetSiblings(ctx, n.ID); return e }},
		{"projects", func() error { _, e := svc.DistinctProjects(ctx); return e }},
		{"list", func() error {
			_, _, e := svc.ListNodes(ctx, service.NodeFilter{}, service.ListOptions{Limit: 10})
			return e
		}},
	} {
		t.Run("closed_"+test.name, func(t *testing.T) { require.Error(t, test.query()) })
	}
}

type apiAnnotationFailure struct{ store.Store }

func (apiAnnotationFailure) SetAnnotations(context.Context, string, []model.Annotation) error {
	return model.ErrConflict
}
func TestAPIWorkflowServices_BackendFailuresDoNotBroadcast(t *testing.T) {
	svc, st, bc := newTestNodeService(t)
	ctx := context.Background()
	n, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Title: "annotation", Project: "TEST"})
	require.NoError(t, err)
	failing := service.NewNodeService(apiAnnotationFailure{st}, bc, nil, nil, time.Now)
	bc.events = nil
	require.ErrorIs(t, failing.AppendAnnotation(ctx, n.ID, model.Annotation{Text: "hello"}), model.ErrConflict)
	require.Empty(t, bc.events)
	require.ErrorIs(t, failing.AppendAnnotation(ctx, "TEST-999", model.Annotation{}), model.ErrNotFound)
	require.ErrorIs(t, svc.UnclaimNode(ctx, "TEST-999", "reason", "writer"), model.ErrNotFound)
	require.ErrorIs(t, svc.ForceReclaimNode(ctx, "TEST-999", "writer", time.Hour), model.ErrNotFound)
	require.ErrorIs(t, svc.CancelNode(ctx, "TEST-999", "reason", "writer", false), model.ErrNotFound)
	require.Empty(t, bc.events)
}
func TestAPIDependencyService_ReadAndBackendFailures(t *testing.T) {
	_, st, bc := newTestNodeService(t)
	svc := service.NewDependencyService(st, bc, nil, time.Now)
	ctx := context.Background()
	_, err := svc.GetBlockers(ctx, "TEST-999")
	require.NoError(t, err)
	require.NoError(t, st.Close())
	_, err = svc.GetBlockers(ctx, "TEST-999")
	require.Error(t, err)
	require.Error(t, svc.AddDependency(ctx, &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeRelated}))
	require.Empty(t, bc.events)
}
func TestAPIAdminService_BackupFailureAndUIDDiagnosticFailure(t *testing.T) {
	_, st, _ := newTestNodeService(t)
	admin := service.NewAdminService(st)
	ctx := context.Background()
	_, err := admin.Backup(ctx, "")
	require.Error(t, err)
	// On this disposable fixture, structural integrity remains healthy while
	// the absent node table forces the second UID diagnostic to report failure.
	_, err = st.WriteDB().ExecContext(ctx, `DROP TABLE nodes`)
	require.NoError(t, err)
	_, err = admin.Verify(ctx)
	require.Error(t, err)
}

func TestAPIWorkflowServices_ReclaimAndAnnotationCommitEvents(t *testing.T) {
	svc, st, bc := newTestNodeService(t)
	ctx := context.Background()
	n, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Title: "stale work", Project: "TEST"})
	require.NoError(t, err)
	require.NoError(t, st.ClaimNode(ctx, n.ID, "old"))
	_, err = st.WriteDB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`, "2000-01-01T00:00:00Z", "old")
	require.NoError(t, err)
	bc.events = nil
	require.NoError(t, svc.ForceReclaimNode(ctx, n.ID, "new", time.Hour))
	got, err := st.GetNode(ctx, n.ID)
	require.NoError(t, err)
	require.Equal(t, "new", got.Assignee)
	require.Len(t, bc.events, 1)
	require.Equal(t, service.EventNodeClaimed, bc.events[0].Type)
	annotation := model.Annotation{ID: "annotation-id", Author: "new", Text: "persisted"}
	require.NoError(t, svc.AppendAnnotation(ctx, n.ID, annotation))
	got, err = st.GetNode(ctx, n.ID)
	require.NoError(t, err)
	require.Equal(t, []model.Annotation{annotation}, got.Annotations)
	require.Len(t, bc.events, 2)
	require.Equal(t, service.EventNodeUpdated, bc.events[1].Type)
}
