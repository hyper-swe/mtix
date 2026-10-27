// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestWakeBudgetMirror_ForeignAndWrongShape_RemainUntouched(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrong payload type", true: "foreign host"}[foreign], func(t *testing.T) {
			state, key, _, file := seededWakeBudget(t)
			if foreign {
				raw, err := os.ReadFile(file)
				require.NoError(t, err)
				var envelope map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(raw, &envelope))
				envelope["host_id"] = json.RawMessage(`"00000000000000000000000000000000"`)
				encoded, err := json.Marshal(envelope)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(file, encoded, 0o600))
			} else {
				require.NoError(t, state.WriteJSON(strings.TrimSuffix(filepath.Base(file), ".json"), "not a mirror"))
			}
			before, err := os.ReadFile(file)
			require.NoError(t, err)
			err = state.UpdateWakeBudget(t.Context(), key, func(*oplocal.WakeBudgetMirror, func() error) error {
				t.Error("untrusted record reached callback")
				return nil
			})
			require.Error(t, err)
			after, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestWakeBudgetMirror_InvalidUpdateOrCancelledCheckpoint_DoesNotOverwrite(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid data", true: "cancelled checkpoint"}[cancelled], func(t *testing.T) {
			state, key, _, file := seededWakeBudget(t)
			before, err := os.ReadFile(file)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err = state.UpdateWakeBudget(ctx, key, func(m *oplocal.WakeBudgetMirror, save func() error) error {
				if cancelled {
					cancel()
				} else {
					m.Version = 0
				}
				return save()
			})
			require.Error(t, err)
			if cancelled {
				require.ErrorIs(t, err, context.Canceled)
			}
			after, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestWakeBudgetMirror_LockWaitHonorsCancellation(t *testing.T) {
	state, key, _, _ := seededWakeBudget(t)
	locked, release, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		result <- state.UpdateWakeBudget(t.Context(), key, func(*oplocal.WakeBudgetMirror, func() error) error { close(locked); <-release; return nil })
	}()
	<-locked
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	err := state.UpdateWakeBudget(ctx, key, func(*oplocal.WakeBudgetMirror, func() error) error { t.Error("contending update ran"); return nil })
	close(release)
	require.NoError(t, <-result)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWakeBudgetMirror_InvalidKeyAndNilUpdate_CreateNothing(t *testing.T) {
	state := testutil.OperatorState(t)
	key := model.WakeRecipient{AgentID: "worker", HostID: "host", UID: "user"}
	require.Error(t, state.UpdateWakeBudget(t.Context(), key, nil))
	key.AgentID = "\n"
	_, _, err := state.ReadWakeBudget(key)
	require.Error(t, err)
	require.Error(t, state.UpdateWakeBudget(t.Context(), key, nil))
	dir, err := state.Path()
	require.NoError(t, err)
	_, err = os.Stat(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestWakeBudgetMirror_UnsafeDirectoryOrLock_RefusesWithoutChangingRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode checks")
	}
	for _, name := range []string{"directory", "lock"} {
		t.Run(name, func(t *testing.T) {
			state, key, _, record := seededWakeBudget(t)
			dir, err := state.Path()
			require.NoError(t, err)
			path, restore := dir, os.FileMode(0o700)
			if name == "lock" {
				path, restore = filepath.Join(dir, "wake-budgets.lock"), 0o600
			}
			before, err := os.ReadFile(record)
			require.NoError(t, err)
			require.NoError(t, os.Chmod(path, 0o777))
			t.Cleanup(func() { require.NoError(t, os.Chmod(path, restore)) })
			err = state.UpdateWakeBudget(t.Context(), key, func(*oplocal.WakeBudgetMirror, func() error) error {
				t.Error("unsafe accounting access reached admission")
				return nil
			})
			require.Error(t, err)
			after, err := os.ReadFile(record)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
