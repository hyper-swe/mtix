// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// autoClaimCase describes one FR-11.2a ownership scenario.
type autoClaimCase struct {
	name         string
	autoClaim    bool
	creator      string
	explicit     string
	wantStatus   model.Status
	wantAssignee string
}

// autoClaimCases covers the creator rule shared by create and decompose (FR-11.2a, MTIX-258).
// The parent is always in_progress and assigned to "owner".
var autoClaimCases = []autoClaimCase{
	{"owner creates: inherits and claims", true, "owner", "", model.StatusInProgress, "owner"},
	{"other creator: open and unassigned", true, "orchestrator", "", model.StatusOpen, ""},
	{"empty creator: open and unassigned", true, "", "", model.StatusOpen, ""},
	{"explicit assign wins for owner", true, "owner", "someone-else", model.StatusInProgress, "someone-else"},
	{"explicit assign wins for other creator", true, "orchestrator", "lane-2", model.StatusInProgress, "lane-2"},
	{"auto_claim off never claims for owner", false, "owner", "", model.StatusOpen, ""},
	{"auto_claim off never claims for other", false, "orchestrator", "", model.StatusOpen, ""},
	{"auto_claim off keeps explicit assign", false, "owner", "lane-2", model.StatusInProgress, "lane-2"},
}

// newClaimedParent builds a service and an in_progress parent created by "planner" and assigned to "owner".
func newClaimedParent(t *testing.T, autoClaim bool) (*service.NodeService, *sqlite.Store, *model.Node, func() time.Time) {
	t.Helper()
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	clock := fixedClock(now)
	svc := service.NewNodeService(s, newRecordingBroadcaster(),
		&service.StaticConfig{AutoClaimEnabled: autoClaim}, slog.Default(), clock)
	ctx := context.Background()
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Parent", Creator: "planner"})
	require.NoError(t, err)
	require.NoError(t, s.ClaimNode(ctx, parent.ID, "owner"))
	return svc, s, parent, clock
}

// TestCreateNode_UnderClaimedParent_ClaimsOnlyForParentAssignee verifies FR-11.2a for create.
func TestCreateNode_UnderClaimedParent_ClaimsOnlyForParentAssignee(t *testing.T) {
	for _, tt := range autoClaimCases {
		t.Run(tt.name, func(t *testing.T) {
			svc, s, parent, clock := newClaimedParent(t, tt.autoClaim)
			ctx := context.Background()
			child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
				ParentID: parent.ID, Project: "PROJ", Title: "Child", Creator: tt.creator, Assignee: tt.explicit,
			})
			require.NoError(t, err)
			got, err := s.GetNode(ctx, child.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantAssignee, got.Assignee)
			assert.Equal(t, tt.wantStatus, child.Status, "returned node matches stored node")
			assert.Equal(t, tt.wantAssignee, child.Assignee)
			assertReadyListing(t, s, clock, got.ID, tt.wantAssignee == "")
		})
	}
}

// TestDecompose_UnderClaimedParent_ClaimsOnlyForParentAssignee verifies FR-11.2a for decompose.
func TestDecompose_UnderClaimedParent_ClaimsOnlyForParentAssignee(t *testing.T) {
	for _, tt := range autoClaimCases {
		if tt.explicit != "" {
			continue // decompose has no per-child assignee input
		}
		t.Run(tt.name, func(t *testing.T) {
			svc, s, parent, clock := newClaimedParent(t, tt.autoClaim)
			ctx := context.Background()
			ids, err := svc.Decompose(ctx, parent.ID, []service.DecomposeInput{{Title: "A"}, {Title: "B"}}, tt.creator)
			require.NoError(t, err)
			require.Len(t, ids, 2)
			for _, id := range ids {
				got, err := s.GetNode(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, tt.wantStatus, got.Status)
				assert.Equal(t, tt.wantAssignee, got.Assignee)
				assertReadyListing(t, s, clock, id, tt.wantAssignee == "")
			}
		})
	}
}

// assertReadyListing checks whether mtix ready (GetReadyNodes) lists the node.
func assertReadyListing(t *testing.T, s *sqlite.Store, clock func() time.Time, id string, wantListed bool) {
	t.Helper()
	bg := service.NewBackgroundService(s, &service.StaticConfig{}, slog.Default(), clock)
	ready, err := bg.GetReadyNodes(context.Background())
	require.NoError(t, err)
	listed := false
	for _, n := range ready {
		if n.ID == id {
			listed = true
		}
	}
	assert.Equal(t, wantListed, listed, "ready listing of %s", id)
}
