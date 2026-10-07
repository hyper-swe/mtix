// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/validator"
	"github.com/hyper-swe/mtix/internal/sync/workflow"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeService_ResolveUIDUsesOwnedSQLite(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	node, err := svc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "READ", Title: "UID owner", Creator: "admin"})
	require.NoError(t, err)
	id, err := svc.ResolveDisplayPathByUID(t.Context(), node.UID)
	require.NoError(t, err)
	require.Equal(t, node.ID, id)
	require.NoError(t, st.Close())
	_, err = svc.ResolveDisplayPathByUID(t.Context(), node.UID)
	require.Error(t, err)
	var operation interface{ Operation() string }
	require.ErrorAs(t, err, &operation)
	require.Equal(t, "resolve node UID", operation.Operation())
}

func TestSyncService_DetectStateUsesOwnedSQLite(t *testing.T) {
	t.Setenv("MTIX_SYNC_DSN", "")
	svc, st, dir := newTestSyncService(t)
	report, err := svc.DetectState(t.Context(), filepath.Join(dir, ".mtix"))
	require.NoError(t, err)
	require.Equal(t, workflow.StateSolo, report.State)
	require.NoError(t, st.Close())
	_, err = svc.DetectState(t.Context(), filepath.Join(dir, ".mtix"))
	require.Error(t, err)
	var operation interface{ Operation() string }
	require.ErrorAs(t, err, &operation)
	require.Equal(t, "detect sync workflow", operation.Operation())
}

func TestPayloadWarnings_BridgePreservesMutationAndDrain(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	node, err := svc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "READ", Title: "warning bridge", Creator: "admin"})
	require.NoError(t, err)
	collector := &service.PayloadWarnings{}
	ctx := service.WithPayloadWarnings(t.Context(), collector)
	prompt := strings.Repeat("p", validator.MaxPayloadBytes+100)
	require.NoError(t, svc.ApplyUpdate(ctx, node.ID, &service.NodeUpdate{Prompt: &prompt}))
	warnings := collector.Drain()
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "WARN: "+node.ID+": the prompt field")
	require.Contains(t, warnings[0], "65536-byte sync limit; saved locally")
	require.Empty(t, collector.Drain())
	persisted, err := svc.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	require.Equal(t, prompt, persisted.Prompt)
}
