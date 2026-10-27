// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Effective settings retain legacy consumers while adding host and agent scope.
package oplocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestWakeSettings_NestedHost_LegacyReadersUseEffectiveSettings(t *testing.T) {
	state := identityState(t)
	require.NoError(t, state.WriteJSON("wake", map[string]any{
		"max_events": 9, "inbox.strict_board_identity": false,
		"host":   map[string]any{"wake.envelope_max_events": 8, "inbox.strict_board_identity": true},
		"agents": map[string]any{"worker": map[string]any{"wake.envelope_max_events": 2, "inbox.strict_board_identity": false}},
	}))
	caps, _, err := state.EnvelopeCaps()
	require.NoError(t, err)
	require.Equal(t, 8, caps.MaxEvents)
	strict, _, err := state.StrictBoardIdentity()
	require.NoError(t, err)
	require.True(t, strict)
	values, _, err := state.WakeSettings("worker")
	require.NoError(t, err)
	require.Equal(t, "2", values["wake.envelope_max_events"].Value)
	require.Equal(t, "agent:worker", values["wake.envelope_max_events"].Source)
	require.Equal(t, "false", values["inbox.strict_board_identity"].Value)
	caps, _, err = state.EnvelopeCapsForAgent("worker")
	require.NoError(t, err)
	require.Equal(t, 2, caps.MaxEvents)
	strict, _, err = state.StrictBoardIdentityForAgent("worker")
	require.NoError(t, err)
	require.False(t, strict)
}

func TestWakeSettings_CorruptContainersAndDisabledGuards_Refused(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  any
	}{
		{"null record", nil}, {"array record", []int{1}}, {"null host", map[string]any{"host": nil}},
		{"null agents", map[string]any{"agents": nil}}, {"null agent", map[string]any{"agents": map[string]any{"worker": nil}}},
		{"disabled guard", map[string]any{"host": map[string]any{"wake.max_hop_depth": 0}}},
		{"negative guard", map[string]any{"host": map[string]any{"wake.backoff_after_unacked": -1}}},
		{"null cap", map[string]any{"host": map[string]any{"wake.wakes_per_hour": nil}}},
		{"host array", map[string]any{"host": []int{1}}},
		{"agents array", map[string]any{"agents": []int{1}}},
		{"bad boolean", map[string]any{"host": map[string]any{"wake.live": "false"}}},
		{"null boolean", map[string]any{"host": map[string]any{"wake.live": nil}}},
		{"fractional cap", map[string]any{"host": map[string]any{"wake.wakes_per_hour": 1.5}}},
		{"bad legacy cap", map[string]any{"max_events": "2"}},
		{"small framing", map[string]any{"host": map[string]any{"wake.envelope_max_total_bytes": 1}}},
		{"bad other agent", map[string]any{"agents": map[string]any{"other": map[string]any{"wake.max_hop_depth": 0}}}},
		{"invalid agent name", map[string]any{"agents": map[string]any{"bad\nagent": map[string]any{}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := identityState(t)
			require.NoError(t, s.WriteJSON("wake", tt.raw))
			_, _, err := s.WakeSettings("")
			require.Error(t, err)
		})
	}
}

func TestWakeSettings_ConcurrentScopedWrites_NoLostValues(t *testing.T) {
	s := identityState(t)
	require.NoError(t, s.WriteJSON("wake", map[string]any{"retained": "preserve"}))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value := fmt.Sprint(i + 1)
			errs <- s.UpdateWakeSetting(t.Context(), WakeSettingUpdate{Agent: fmt.Sprintf("agent-%d", i), Key: "wake.wakes_per_hour", Value: &value}, nil)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for i := 0; i < 8; i++ {
		settings, _, err := s.WakeSettings(fmt.Sprintf("agent-%d", i))
		require.NoError(t, err)
		require.Equal(t, fmt.Sprint(i+1), settings["wake.wakes_per_hour"].Value)
	}
	var raw map[string]any
	_, err := s.ReadJSON("wake", &raw)
	require.NoError(t, err)
	require.Equal(t, "preserve", raw["retained"])
}

func TestWakeSettings_CancelledOrUnauthorizedUpdate_DoesNotCreateState(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			s := identityState(t)
			path, err := s.Path()
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelled {
				cancel()
			}
			value := "64"
			denied := errors.New("operator terminal required")
			err = s.UpdateWakeSetting(ctx, WakeSettingUpdate{Key: "wake.max_hop_depth", Value: &value}, func(string, string) error { return denied })
			if cancelled {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, denied)
			}
			_, err = os.Stat(path)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestWakeSettings_InvalidScopeAndDurableWriteFailure_PreserveState(t *testing.T) {
	s := identityState(t)
	value := "32"
	require.ErrorIs(t, s.UpdateWakeSetting(t.Context(), WakeSettingUpdate{Key: "wake.unknown", Value: &value}, nil), model.ErrInvalidConfigKey)
	require.Error(t, s.UpdateWakeSetting(t.Context(), WakeSettingUpdate{Agent: "bad\nagent", Key: "wake.max_hop_depth", Value: &value}, nil))
	require.NoError(t, s.WriteJSON("wake", map[string]any{"host": map[string]any{"wake.max_hop_depth": 32}, "retained": "keep"}))
	cause := errors.New("durable commit unavailable")
	s.beforeCommit = func() error { return cause }
	changed := "64"
	require.ErrorIs(t, s.UpdateWakeSetting(t.Context(), WakeSettingUpdate{Key: "wake.max_hop_depth", Value: &changed}, nil), cause)
	s.beforeCommit = nil
	values, _, err := s.WakeSettings("")
	require.NoError(t, err)
	require.Equal(t, "32", values["wake.max_hop_depth"].Value)
	bad := "{"
	require.Error(t, s.UpdateWakeSetting(t.Context(), WakeSettingUpdate{Key: "wake.max_hop_depth", Value: &bad}, nil))
	unlimited := ""
	require.NoError(t, s.UpdateWakeSetting(t.Context(), WakeSettingUpdate{Agent: "worker", Key: "wake.wakes_per_hour", Value: &unlimited}, nil))
	agents, err := s.WakeAgents()
	require.NoError(t, err)
	require.Equal(t, []string{"worker"}, agents)
	values, _, err = s.WakeSettings("worker")
	require.NoError(t, err)
	require.Empty(t, values["wake.wakes_per_hour"].Value)
	var raw map[string]json.RawMessage
	_, err = s.ReadJSON("wake", &raw)
	require.NoError(t, err)
	require.JSONEq(t, `"keep"`, string(raw["retained"]))
	invalidState := New(Env{GOOS: "linux", Home: "relative"})
	_, _, err = invalidState.WakeSettings("")
	require.Error(t, err)
	_, err = invalidState.WakeAgents()
	require.Error(t, err)
	require.Error(t, invalidState.UpdateWakeSetting(t.Context(), WakeSettingUpdate{Key: "wake.max_hop_depth", Value: &value}, nil))
}

func TestWakeSettings_HostLiveOffAndOverrideDeletion_InheritSafely(t *testing.T) {
	s := identityState(t)
	require.NoError(t, s.WriteJSON("wake", map[string]any{"host": map[string]any{"wake.live": false, "wake.wakes_per_hour": 12, "future.setting": true}, "agents": map[string]any{"worker": map[string]any{"wake.live": true, "wake.wakes_per_hour": 3}}}))
	values, _, err := s.WakeSettings("worker")
	require.NoError(t, err)
	require.Equal(t, "false", values["wake.live"].Value)
	require.Equal(t, "host", values["wake.live"].Source)
	require.NoError(t, s.UpdateWakeSetting(t.Context(), WakeSettingUpdate{Agent: "worker", Key: "wake.wakes_per_hour"}, nil))
	values, _, err = s.WakeSettings("worker")
	require.NoError(t, err)
	require.Equal(t, "12", values["wake.wakes_per_hour"].Value)
	_, _, err = s.WakeSettings("bad\nagent")
	require.Error(t, err)
	require.NoError(t, s.WriteJSON("wake", map[string]any{"agents": []int{1}}))
	_, err = s.WakeAgents()
	require.Error(t, err)
	require.NoError(t, s.WriteJSON("wake", map[string]any{"agents": map[string]any{"bad\nagent": map[string]any{}}}))
	_, err = s.WakeAgents()
	require.Error(t, err)
}

func TestWakeSettings_RechecksUnderLock_RefusesNewLoosening(t *testing.T) {
	s := identityState(t)
	require.NoError(t, s.WriteJSON("wake", map[string]any{"host": map[string]any{"wake.wakes_per_hour": 12}}))
	value := "11"
	calls := 0
	denied := errors.New("terminal required")
	err := s.UpdateWakeSetting(t.Context(), WakeSettingUpdate{Key: "wake.wakes_per_hour", Value: &value}, func(before, after string) error {
		calls++
		if calls == 1 {
			require.Equal(t, "12", before)
			require.NoError(t, s.WriteJSON("wake", map[string]any{"host": map[string]any{"wake.wakes_per_hour": 5}}))
			return nil
		}
		if model.WakeSettingLoosens("wake.wakes_per_hour", before, after) {
			return denied
		}
		return nil
	})
	require.ErrorIs(t, err, denied)
	require.Equal(t, 2, calls)
	values, _, err := s.WakeSettings("")
	require.NoError(t, err)
	require.Equal(t, "5", values["wake.wakes_per_hour"].Value)
}

func TestWakeSettings_ChangedStateAndUnsafeLock_Refused(t *testing.T) {
	modes := []string{"corrupt after preflight", "cancel after preflight", "unsafe lock"}
	if runtime.GOOS != "windows" {
		modes = append(modes, "unsafe directory")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			s := identityState(t)
			require.NoError(t, s.WriteJSON("wake", map[string]any{"host": map[string]any{"wake.max_hop_depth": 32}}))
			dir, err := s.Path()
			require.NoError(t, err)
			if mode == "unsafe lock" {
				require.NoError(t, os.Mkdir(filepath.Join(dir, "wake-settings.lock"), 0700))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			value := "64"
			err = s.UpdateWakeSetting(ctx, WakeSettingUpdate{Key: "wake.max_hop_depth", Value: &value}, func(string, string) error {
				calls++
				if calls == 1 {
					switch mode {
					case "corrupt after preflight":
						require.NoError(t, s.WriteJSON("wake", []int{1}))
					case "cancel after preflight":
						cancel()
					case "unsafe directory":
						require.NoError(t, os.Chmod(dir, 0777))
						t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0700)) })
					}
				}
				return nil
			})
			require.Error(t, err)
		})
	}
}
