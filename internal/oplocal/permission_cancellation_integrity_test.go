// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func reviewR1CanceledPending(t *testing.T) (*State, PermissionBatch, string) {
	t.Helper()
	s, intent := contractAcceptedPending(t, "after:grants.json")
	s.alertStage = func(stage string) error {
		if stage == "after:permission-replay-cancelled" {
			return errors.New("cancellation cleanup interrupted")
		}
		return nil
	}
	require.Error(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	s.alertStage = nil
	return s, intent, strings.TrimSuffix(permissionCancellationName(intent.ID), ".json")
}

func TestCancelPermissionBatch_CorruptAudit_CannotReviveCaller(t *testing.T) {
	kinds := []string{"malformed", "foreign-host", "null", "reason", "state", "caller", "pending-marker", "intent", "preimage", "canonical-time", "symlink"}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) { reviewR1CorruptCancellation(t, kind) })
	}
}

func reviewR1CorruptCancellation(t *testing.T, kind string) {
	t.Helper()
	if kind == "symlink" && os.PathSeparator != '/' {
		t.Skip("native symlink control")
	}
	s, intent, name := reviewR1CanceledPending(t)
	var row permissionCancellation
	reports, err := s.ReadJSON(name, &row)
	require.NoError(t, err)
	require.Empty(t, reports)
	switch kind {
	case "reason":
		row.Reason = ""
	case "state":
		row.BoardLine = "published"
	case "caller":
		row.Pending.Batch.ID = strings.Repeat("2", 32)
	case "pending-marker":
		row.Pending.CompletedID = intent.ID
	case "intent":
		row.Pending.Batch.IntentHash = strings.Repeat("0", 64)
	case "preimage":
		row.Pending.BeforeHash = strings.Repeat("0", 64)
	case "canonical-time":
		row.Pending.Batch.Time = row.Pending.Batch.Time.Add(time.Hour)
	}
	require.NoError(t, s.WriteJSON(name, row))
	reviewR1CorruptCancellationFile(t, s, name, kind, row)
	before := alertStateSnapshot(t, s)
	require.ErrorIs(t, s.CancelPermissionBatch(t.Context(), intent.ID), model.ErrOperatorStateUnreadable)
	require.Equal(t, before, alertStateSnapshot(t, s))
	require.ErrorIs(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil), model.ErrOperatorStateUnreadable)
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func reviewR1CorruptCancellationFile(t *testing.T, s *State, name, kind string, row permissionCancellation) {
	t.Helper()
	switch kind {
	case "malformed":
		writePermissionFixture(t, s, name+".json", []byte("{"))
	case "null":
		require.NoError(t, s.WriteJSON(name, nil))
	case "foreign-host":
		data, err := json.Marshal(record{HostID: strings.Repeat("0", 32), Data: mustPermissionJSON(row)})
		require.NoError(t, err)
		writePermissionFixture(t, s, name+".json", data)
	case "symlink":
		dir, err := s.Path()
		require.NoError(t, err)
		file := filepath.Join(dir, name+".json")
		require.NoError(t, os.Rename(file, file+".saved"))
		require.NoError(t, os.Symlink(file+".saved", file))
	}
}

func TestCancelPermissionBatch_InvalidClock_PreservesAcceptedState(t *testing.T) {
	s, intent := contractAcceptedPending(t, "after:grants.json")
	s.alertStage = nil
	before := alertStateSnapshot(t, s)
	for _, now := range []time.Time{{}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		accessor := NewWithPermissionClock(s.env, func() time.Time { return now })
		require.Error(t, accessor.CancelPermissionBatch(t.Context(), intent.ID))
		require.Equal(t, before, alertStateSnapshot(t, s))
	}
}
