// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestConfig_HistoryMaxEntries_UnsetByDefaultAndPositiveIntegerOnly: unset
// means no trimming; 0, negative and non-integer values are refused and change
// nothing (MTIX-95.31.19.4).
func TestConfig_HistoryMaxEntries_UnsetByDefaultAndPositiveIntegerOnly(t *testing.T) {
	cs, err := service.NewConfigService(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	assert.Zero(t, cs.HistoryMaxEntries(), "unset")
	for _, bad := range []string{"0", "-3", "1.5", "ten", "1e3", " "} {
		_, err := cs.Set("history.max_entries_per_task", bad)
		require.ErrorIs(t, err, model.ErrInvalidInput, bad)
	}
	assert.Zero(t, cs.HistoryMaxEntries())
	_, err = cs.Set("history.max_entries_per_task", "250")
	require.NoError(t, err)
	assert.Equal(t, 250, cs.HistoryMaxEntries())
	_, err = cs.Set("history.max_entries_per_task", "")
	require.NoError(t, err)
	assert.Zero(t, cs.HistoryMaxEntries(), "an empty value unsets the limit")
}

func TestNodeService_GetNodeHistory_ReturnsNewestFirstThroughTheService(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "t", Creator: "tester"})
	require.NoError(t, err)
	got, err := svc.GetNodeHistory(ctx, node.ID, 50)
	require.NoError(t, err)
	for i := 1; i < len(got); i++ {
		assert.False(t, got[i].Timestamp.After(got[i-1].Timestamp), "newest first")
	}
	_, err = svc.GetNodeHistory(ctx, "PROJ-999", 5)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// The additive doctor contract: a row without data serialises exactly as
// before, with four fields; data appears only when a check sets it.
func TestDoctorResult_WithoutData_SerialisesAsTheFourFieldRowByteForByte(t *testing.T) {
	raw, err := json.Marshal(service.DoctorResult{ID: "x.y", Status: service.DoctorPass, Detail: "d", Fix: ""})
	require.NoError(t, err)
	assert.Equal(t, `{"id":"x.y","status":"pass","detail":"d","fix":""}`, string(raw))
	raw, err = json.Marshal(service.DoctorResult{ID: "x.y", Status: service.DoctorInfo, Data: map[string]any{"rows": 3}})
	require.NoError(t, err)
	assert.Equal(t, `{"id":"x.y","status":"info","detail":"","fix":"","data":{"rows":3}}`, string(raw))
}

// The limit is read from the loaded config, not by parsing config.yaml on
// every history write.
func TestConfig_HistoryMaxEntries_DoesNotReparseTheFileOnEveryRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)
	_, err = cs.Set("history.max_entries_per_task", "7")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("history:\n  max_entries_per_task: \"9\"\n"), 0o600))
	assert.Equal(t, 7, cs.HistoryMaxEntries(), "an outside edit applies when the process next loads the config")
}
