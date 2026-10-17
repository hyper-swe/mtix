//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

func TestAttachPending_SandboxedRegister_DrainedByGlue(t *testing.T) {
	f, s, loc := identityFixture(t)
	require.NoError(t, s.Ensure())
	dir, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0700)) })
	_, err = f.svc.RegisterSession(t.Context(), req("lane-a", 1))
	require.NoError(t, err)
	rows, err := f.store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NoError(t, os.Chmod(dir, 0700))
	result, err := f.svc.DrainAttachPending(t.Context(), f.svc.Routing.Board)
	require.NoError(t, err)
	require.Equal(t, 2, result.Processed)
	boards, err := s.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Len(t, boards, 1)
	ids, err := s.LaneIDs(t.Context(), loc)
	require.NoError(t, err)
	require.Equal(t, []string{"lane-a"}, ids)
	rows, err = f.store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
	result, err = f.svc.DrainAttachPending(t.Context(), f.svc.Routing.Board)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	grants, _, err := s.BoardGrants()
	require.NoError(t, err)
	require.Empty(t, grants)
}
func TestAttachPending_ForgedOtherBoard_DroppedWithAlert(t *testing.T) {
	for _, kind := range []string{"other-board", "other-path", "unknown-kind", "outside-lane", "bad-payload", "unknown-field", "trailing-data", "empty-agent"} {
		t.Run(kind, func(t *testing.T) {
			f, s, loc := identityFixture(t)
			payload := map[string]string{"agent_id": "forged", "harness": "codex", "lane_path": loc.Path}
			row := sqlite.AttachPending{Kind: "lane", BoardKey: loc.BoardKey, Path: loc.Root, QueuedAt: "2026-10-12T10:00:00Z"}
			switch kind {
			case "other-board":
				row.BoardKey = "22222222-2222-4222-8222-222222222222"
			case "other-path":
				row.Path = t.TempDir()
			case "unknown-kind":
				row.Kind = "grant"
			case "outside-lane":
				payload["lane_path"] = t.TempDir()
			}
			raw, err := json.Marshal(payload)
			require.NoError(t, err)
			row.Payload = string(raw)
			if kind == "unknown-field" {
				row.Payload = `{"unexpected":true}`
			}
			if kind == "trailing-data" {
				row.Payload += `{}`
			}
			if kind == "empty-agent" {
				row.Payload = `{}`
			}
			if kind == "bad-payload" {
				row.Payload = "{"
			}
			_, err = f.store.EnqueueAttachPending(t.Context(), row)
			require.NoError(t, err)
			result, err := f.svc.DrainAttachPending(t.Context(), f.svc.Routing.Board)
			require.NoError(t, err)
			require.Equal(t, 1, result.Dropped)
			alerts, err := s.RecentAlerts(t.Context(), 0)
			require.NoError(t, err)
			require.Len(t, alerts, 1)
			require.Equal(t, "attach.pending-rejected", alerts[0].Class)
			assertRejectedRoutingUnchanged(t, s, loc)
		})
	}
}
func TestDrainAttachPending_OperatorUnavailable_PreservesRetryRow(t *testing.T) {
	f, s, loc := identityFixture(t)
	require.NoError(t, s.Ensure())
	dir, err := s.Path()
	require.NoError(t, err)
	for _, kind := range []string{"lane", "forged"} {
		raw, _ := json.Marshal(map[string]string{"agent_id": "lane-a", "harness": "codex", "lane_path": loc.Path})
		row := sqlite.AttachPending{Kind: kind, BoardKey: loc.BoardKey, Path: loc.Root, Payload: string(raw), QueuedAt: "now"}
		_, err = f.store.EnqueueAttachPending(t.Context(), row)
		require.NoError(t, err)
	}
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0700)) })
	_, err = f.svc.DrainAttachPending(t.Context(), service.AttachBoard{Key: loc.BoardKey, Path: loc.Root})
	require.Error(t, err)
	rows, err := f.store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func assertRejectedRoutingUnchanged(t *testing.T, s *oplocal.State, loc oplocal.LaneLocation) {
	t.Helper()
	ids, err := s.LaneIDs(t.Context(), loc)
	require.NoError(t, err)
	require.Empty(t, ids)
	boards, err := s.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Empty(t, boards)
	grants, _, err := s.BoardGrants()
	require.NoError(t, err)
	require.Empty(t, grants)
}
