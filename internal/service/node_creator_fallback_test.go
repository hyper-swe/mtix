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

// claimedByFallbackParent builds a service with auto-claim on and an in_progress
// parent assigned to owner, which may equal a transport fallback label (FR-11.2a).
func claimedByFallbackParent(t *testing.T, owner string) (*service.NodeService, *sqlite.Store, *recordingBroadcaster, string) {
	t.Helper()
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	// Use a fixed sync identity so claim tests need no host network interfaces.
	_, seedErr := s.WriteDB().ExecContext(t.Context(), "UPDATE meta SET value = ? WHERE key = ?", "0123456789abcdef", "meta.sync.machine_hash")
	require.NoError(t, seedErr)
	bc := newRecordingBroadcaster()
	svc := service.NewNodeService(s, bc, &service.StaticConfig{AutoClaimEnabled: true}, slog.Default(),
		fixedClock(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)))
	parent, err := svc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "PROJ", Title: "Parent", Creator: "planner"})
	require.NoError(t, err)
	require.NoError(t, s.ClaimNode(context.Background(), parent.ID, owner))
	return svc, s, bc, parent.ID
}

// creatorFallbackCases: a fallback label is audit attribution only, so it never
// inherits a claim even when it equals the parent's assignee; a supplied creator
// wins over the fallback.
var creatorFallbackCases = []struct {
	name         string
	creator      string
	fallback     string
	owner        string
	wantCreator  string
	wantStatus   model.Status
	wantAssignee string
}{
	{"fallback equals parent assignee", "", "cli", "cli", "cli", model.StatusOpen, ""},
	{"fallback differs from parent assignee", "", "mcp", "owner", "mcp", model.StatusOpen, ""},
	{"supplied creator equal to assignee inherits", "owner", "cli", "owner", "owner", model.StatusInProgress, "owner"},
	{"supplied creator beats a matching fallback", "other", "owner", "owner", "other", model.StatusOpen, ""},
}

func TestCreateNode_CreatorFallback_RecordsAttributionAndNeverInherits(t *testing.T) {
	for _, tt := range creatorFallbackCases {
		t.Run(tt.name, func(t *testing.T) {
			svc, s, _, parentID := claimedByFallbackParent(t, tt.owner)
			child, err := svc.CreateNode(context.Background(), &service.CreateNodeRequest{
				ParentID: parentID, Project: "PROJ", Title: "Child", Creator: tt.creator, CreatorFallback: tt.fallback})
			require.NoError(t, err)
			got, err := s.GetNode(context.Background(), child.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCreator, got.Creator)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantAssignee, got.Assignee)
		})
	}
}

func TestDecomposeWithCreatorFallback_RecordsAttributionAndNeverInherits(t *testing.T) {
	for _, tt := range creatorFallbackCases {
		t.Run(tt.name, func(t *testing.T) {
			svc, s, bc, parentID := claimedByFallbackParent(t, tt.owner)
			bc.Reset()
			ids, err := svc.DecomposeWithCreatorFallback(context.Background(), parentID,
				[]service.DecomposeInput{{Title: "A"}}, tt.creator, tt.fallback)
			require.NoError(t, err)
			require.Len(t, ids, 1)
			got, err := s.GetNode(context.Background(), ids[0])
			require.NoError(t, err)
			assert.Equal(t, tt.wantCreator, got.Creator)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantAssignee, got.Assignee)
			// The parent's progress event is attributed to the creator, else the fallback.
			progress := 0
			for _, ev := range bc.Events() {
				if ev.Type == service.EventProgressChanged && ev.NodeID == parentID {
					progress++
					assert.Equal(t, tt.wantCreator, ev.Author)
				}
			}
			assert.Equal(t, 1, progress, "one progress.changed event for the parent")
		})
	}
}

// TestRelayQuarantineResolver_Recreate_ActorIsAttributionAndNeverInherits: the
// actor of a recreate (the CLI label cli when no author is configured) is
// attribution only, so a task recreated under a parent claimed by that same
// name comes out open and unassigned (FR-11.2a, MTIX-278).
func TestRelayQuarantineResolver_Recreate_ActorIsAttributionAndNeverInherits(t *testing.T) {
	for _, actor := range []string{"cli", "a"} {
		t.Run("parent claimed by "+actor, func(t *testing.T) {
			nodes, st, _, parentID := claimedByFallbackParent(t, actor)
			res := service.NewRelayQuarantineResolver(st, nodes, fixedClock(time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)))
			h := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: parentID + ".1", parent: parentID, rs: 4})
			out, err := res.Resolve(context.Background(), service.RelayResolveRequest{Handle: h, Mode: service.RelayResolveRecreate, Actor: actor, Reason: "recreate after collision"})
			require.NoError(t, err)
			got, err := st.GetNode(context.Background(), out.RecreatedAs)
			require.NoError(t, err)
			assert.Equal(t, actor, got.Creator, "the actor is recorded as the creator")
			assert.Equal(t, model.StatusOpen, got.Status)
			assert.Empty(t, got.Assignee)
		})
	}
}
