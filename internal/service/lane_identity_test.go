// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
)

func identityFixture(t *testing.T) (*regFixture, *oplocal.State, oplocal.LaneLocation) {
	t.Helper()
	f := newRegFixture(t)
	s := operatorAlertsFixture(t)
	root := t.TempDir()
	loc := oplocal.LaneLocation{Harness: "codex", BoardKey: "11111111-1111-4111-8111-111111111111", Root: root, Path: root}
	f.svc.Routing = &service.AttachRouting{State: s, Board: service.AttachBoard{Key: loc.BoardKey, Path: root}, LanePath: root}
	return f, s, loc
}

func TestResolveAgentID_EnvVarWins(t *testing.T) {
	f, s, loc := identityFixture(t)
	require.NoError(t, s.LearnLane(t.Context(), loc, "learned"))
	got, err := f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{AgentID: "explicit", SessionHandle: "h1"})
	require.NoError(t, err)
	require.Equal(t, "explicit", got.ID)
	require.True(t, got.Silent)
}
func TestResolveAgentID_ResumedHandle_WinsOverLearned(t *testing.T) {
	f, s, loc := identityFixture(t)
	_, err := f.svc.RegisterSession(t.Context(), req("resumed", 1))
	require.NoError(t, err)
	require.NoError(t, s.LearnLane(t.Context(), loc, "learned"))
	got, err := f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{SessionHandle: "h1"})
	require.NoError(t, err)
	require.Equal(t, "resumed", got.ID)
	require.True(t, got.Silent)
}
func TestResolveAgentID_TwoLearnedIDs_ProposesNeverPicks(t *testing.T) {
	f, s, loc := identityFixture(t)
	for _, id := range []string{"lane-a", "lane-b"} {
		require.NoError(t, s.LearnLane(t.Context(), loc, id))
	}
	for range 2 {
		got, err := f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{})
		require.NoError(t, err)
		require.Empty(t, got.ID)
		require.False(t, got.Silent)
		require.Equal(t, []string{"lane-a", "lane-b"}, got.Proposals)
	}
}
func TestResolveAgentID_SingleLearnedID_RequiresNoLiveOrUnknownRegistration(t *testing.T) {
	for _, tc := range []struct {
		name            string
		live            service.LiveState
		row, wantSilent bool
	}{
		{"absent", service.LiveUnknown, false, true}, {"dead", service.LiveDead, true, true}, {"alive", service.LiveAlive, true, false}, {"unknown", service.LiveUnknown, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, loc := identityFixture(t)
			require.NoError(t, s.LearnLane(t.Context(), loc, "lane-a"))
			if tc.row {
				_, err := f.svc.RegisterSession(t.Context(), req("lane-a", 1))
				require.NoError(t, err)
				f.live[1] = tc.live
			}
			got, err := f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{})
			require.NoError(t, err)
			require.Equal(t, tc.wantSilent, got.Silent)
			if tc.wantSilent {
				require.Equal(t, "lane-a", got.ID)
			} else {
				require.Empty(t, got.ID)
			}
		})
	}
}

func TestResolveAgentID_TwoEverLearnedOneAvailable_RemainsProposal(t *testing.T) {
	f, s, loc := identityFixture(t)
	for _, id := range []string{"lane-a", "lane-b"} {
		require.NoError(t, s.LearnLane(t.Context(), loc, id))
	}
	_, err := f.svc.RegisterSession(t.Context(), req("lane-a", 1))
	require.NoError(t, err)
	f.live[1] = service.LiveAlive
	got, err := f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{Proposals: []string{"lane-a", "candidate", "candidate", "bad\nid"}})
	require.NoError(t, err)
	require.False(t, got.Silent)
	require.Empty(t, got.ID)
	require.Equal(t, []string{"candidate", "lane-b"}, got.Proposals)
}
func TestResolveAgentID_SharedResumedHandle_ProposesBothIDs(t *testing.T) {
	f, _, loc := identityFixture(t)
	for _, id := range []string{"lane-a", "lane-b"} {
		_, err := f.svc.RegisterSession(t.Context(), req(id, 1))
		require.NoError(t, err)
	}
	got, err := f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{SessionHandle: "h1"})
	require.NoError(t, err)
	require.Empty(t, got.ID)
	require.False(t, got.Silent)
	require.Equal(t, []string{"lane-a", "lane-b"}, got.Proposals)
}
func TestResolveAgentID_InvalidEnvironmentOrOtherBoard_ReturnsError(t *testing.T) {
	f, _, loc := identityFixture(t)
	_, err := f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{AgentID: "bad\nid"})
	require.Error(t, err)
	loc.Root = t.TempDir()
	_, err = f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{})
	require.Error(t, err)
}
