// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Two-store tests for MTIX-95.31.2 (FR-15.2i): a field a teammate clears on
// purpose (unclaim, reopen, undefer) applies, because the teammate's board
// holds every activity entry of the node, so it descends from the local
// copy. A stale board that clears a field this store set later, and a
// board an older client re-exported (schema 1.0.0, no activity), are
// refused. Written red-first.
package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// shareBoard copies from's .mtix/tasks.json to to's, as a git push and pull
// between two clones do.
func shareBoard(t *testing.T, from, to *guardFixture) {
	t.Helper()
	to.pull(t, from.read(t, "tasks.json"))
}

// teammateClone returns a teammate's clone that has imported a's board.
func teammateClone(t *testing.T, a *guardFixture) *guardFixture {
	t.Helper()
	b := newProjectFixture(t)
	shareBoard(t, a, b)
	require.NoError(t, b.svc.AutoImport(context.Background(), b.mtixDir))
	_, err := b.store.GetNode(context.Background(), "PROJ-2")
	require.NoError(t, err, "the teammate's clone holds the board")
	return b
}

// TestAutoImport_TeammateClearsFieldOnPurpose_Applies verifies unclaim,
// reopen and undefer by a teammate reach this store: the cleared field is
// imported, not refused.
func TestAutoImport_TeammateClearsFieldOnPurpose_Applies(t *testing.T) {
	future := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		local    func(t *testing.T, st *sqlite.Store)
		teammate func(t *testing.T, st *sqlite.Store)
		check    func(t *testing.T, n *model.Node)
	}{
		{"unclaim", func(t *testing.T, st *sqlite.Store) {
			require.NoError(t, st.ClaimNode(context.Background(), "PROJ-2", "agent-a"))
		}, func(t *testing.T, st *sqlite.Store) {
			require.NoError(t, st.UnclaimNode(context.Background(), "PROJ-2", "handing back", "agent-b"))
		}, func(t *testing.T, n *model.Node) {
			assert.Empty(t, n.Assignee)
			assert.Equal(t, model.StatusOpen, n.Status)
		}},
		{"reopen", func(t *testing.T, st *sqlite.Store) {
			require.NoError(t, st.ClaimNode(context.Background(), "PROJ-2", "agent-a"))
			require.NoError(t, st.TransitionStatus(context.Background(), "PROJ-2", model.StatusDone, "done", "agent-a"))
		}, func(t *testing.T, st *sqlite.Store) {
			require.NoError(t, st.TransitionStatus(context.Background(), "PROJ-2", model.StatusOpen, "not done", "agent-b"))
		}, func(t *testing.T, n *model.Node) {
			assert.Nil(t, n.ClosedAt)
			assert.Equal(t, model.StatusOpen, n.Status)
		}},
		{"undefer", func(t *testing.T, st *sqlite.Store) {
			require.NoError(t, st.DeferNode(context.Background(), "PROJ-2", &future, "later", "agent-a"))
		}, func(t *testing.T, st *sqlite.Store) {
			require.NoError(t, st.TransitionStatus(context.Background(), "PROJ-2", model.StatusOpen, "now", "agent-b"))
		}, func(t *testing.T, n *model.Node) {
			assert.Nil(t, n.DeferUntil)
			assert.Equal(t, model.StatusOpen, n.Status)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			a := newGuardFixture(t)
			tt.local(t, a.store)
			require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
			b := teammateClone(t, a)
			tt.teammate(t, b.store)
			require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))

			shareBoard(t, b, a)
			require.NoError(t, a.svc.AutoImport(ctx, a.mtixDir), a.notices.String())
			assert.Contains(t, a.notices.String(), "mtix: imported the changed .mtix/tasks.json")
			node, err := a.store.GetNode(ctx, "PROJ-2")
			require.NoError(t, err)
			tt.check(t, node)
			assert.Equal(t, 2, a.annotationCount(t, "PROJ-1"))
		})
	}
}

// TestAutoImport_StaleOrOlderClientBoardClearsField_Refused verifies the two
// cases a cleared field is a loss: the teammate's board predates a local
// claim (it lacks the claim's activity entry), or an older client
// re-exported it (schema 1.0.0, no activity). Both are refused and the
// local claim is kept.
func TestAutoImport_StaleOrOlderClientBoardClearsField_Refused(t *testing.T) {
	tests := []struct {
		name  string
		board func(t *testing.T, a, b *guardFixture) []byte
		want  string
	}{
		{"stale board", func(t *testing.T, a, b *guardFixture) []byte {
			// b cloned before a's claim, then retitled PROJ-1.
			title := "Retitled by the teammate"
			require.NoError(t, b.store.UpdateNode(context.Background(), "PROJ-1", &store.NodeUpdate{Title: &title}))
			require.NoError(t, b.svc.AutoExport(context.Background(), b.mtixDir))
			return b.read(t, "tasks.json")
		}, "PROJ-2: 1 activity entry, fields agent_state, assignee"},
		{"older client re-export", func(t *testing.T, a, b *guardFixture) []byte {
			// b saw the claim, but an older client wrote the board.
			shareBoard(t, a, b)
			require.NoError(t, b.svc.AutoImport(context.Background(), b.mtixDir))
			require.NoError(t, b.store.UnclaimNode(context.Background(), "PROJ-2", "handing back", "agent-b"))
			require.NoError(t, b.svc.AutoExport(context.Background(), b.mtixDir))
			return asOlderClientBoard(t, b.read(t, "tasks.json"))
		}, "fields agent_state, assignee"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			a := newGuardFixture(t)
			b := teammateClone(t, a)
			require.NoError(t, a.store.ClaimNode(ctx, "PROJ-2", "agent-a"))
			require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
			board := tt.board(t, a, b)

			a.pull(t, board)
			err := a.svc.AutoImport(ctx, a.mtixDir)
			require.ErrorIs(t, err, service.ErrAutoImportRefused)
			assert.Contains(t, a.notices.String(), tt.want)
			assert.Equal(t, 1, strings.Count(a.notices.String(), refusalHeader))
			node, getErr := a.store.GetNode(ctx, "PROJ-2")
			require.NoError(t, getErr)
			assert.Equal(t, "agent-a", node.Assignee, "the local claim is kept")
		})
	}
}

// TestAutoImport_StaleBoardUndoesWriteWithoutActivity_Refused is the round-3
// regression: mtix update and mtix delete write no activity entry, so a
// teammate's board taken before such a write still holds every activity
// entry of the node. Its copy of the node is older (updated_at), so the
// values it leaves empty are losses and the import is refused.
func TestAutoImport_StaleBoardUndoesWriteWithoutActivity_Refused(t *testing.T) {
	tests := []struct {
		name  string
		write func(t *testing.T, st *sqlite.Store)
		want  string
	}{
		{"update --assignee", func(t *testing.T, st *sqlite.Store) {
			assignee := "agent-local"
			require.NoError(t, st.UpdateNode(context.Background(), "PROJ-2", &store.NodeUpdate{Assignee: &assignee}))
		}, "PROJ-2: field assignee"},
		{"update --description", func(t *testing.T, st *sqlite.Store) {
			description := "Local notes"
			require.NoError(t, st.UpdateNode(context.Background(), "PROJ-2", &store.NodeUpdate{Description: &description}))
		}, "PROJ-2: field description"},
		{"delete", func(t *testing.T, st *sqlite.Store) {
			require.NoError(t, st.DeleteNode(context.Background(), "PROJ-2", false, "agent-local"))
		}, "PROJ-2: fields deleted_at, deleted_by"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			a := newGuardFixture(t)
			b := teammateClone(t, a) // cloned before the write
			tt.write(t, a.store)
			require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
			before := a.storeSnapshot(t)

			title := "Retitled by the teammate"
			require.NoError(t, b.store.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{Title: &title}))
			require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
			shareBoard(t, b, a)

			err := a.svc.AutoImport(ctx, a.mtixDir)
			require.ErrorIs(t, err, service.ErrAutoImportRefused, a.notices.String())
			assert.Contains(t, a.notices.String(), tt.want)
			assert.Equal(t, before, a.storeSnapshot(t), "the local write is kept")
		})
	}
}

// TestAutoImport_TeammateLaterUpdate_Applies verifies a teammate's genuine
// later change still applies: they cloned after the local write and cleared
// the value themselves, so their copy is not older.
func TestAutoImport_TeammateLaterUpdate_Applies(t *testing.T) {
	ctx := context.Background()
	a := newGuardFixture(t)
	assignee := "agent-local"
	require.NoError(t, a.store.UpdateNode(ctx, "PROJ-2", &store.NodeUpdate{Assignee: &assignee}))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	b := teammateClone(t, a) // cloned after the write
	cleared := ""
	require.NoError(t, b.store.UpdateNode(ctx, "PROJ-2", &store.NodeUpdate{Assignee: &cleared}))
	require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
	shareBoard(t, b, a)

	require.NoError(t, a.svc.AutoImport(ctx, a.mtixDir), a.notices.String())
	node, err := a.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Empty(t, node.Assignee, "the teammate's later clear applies")
}
