// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Host wake configuration defaults and guard validation implement FR-20.3.
package service_test

import (
	"errors"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/testutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestWakeSettings_Defaults_UnlimitedBudgetsAndAlwaysOnGuards(t *testing.T) {
	cs := wakeConfig(t, testutil.OperatorState(t), "", false)
	for _, tt := range []struct{ key, want string }{
		{"wake.live", "true"},
		{"wake.wakes_per_hour", ""},
		{"wake.per_sender_per_hour", ""},
		{"wake.remote_awake_daily_cap", ""},
		{"wake.reply_extensions_per_hour", ""},
		{"wake.envelope_max_events", ""},
		{"wake.envelope_max_body_bytes", ""},
		{"wake.envelope_max_total_bytes", ""},
		{"wake.max_hop_depth", "32"},
		{"wake.backoff_after_unacked", "5"},
		{"inbox.strict_board_identity", "false"},
	} {
		t.Run(tt.key, func(t *testing.T) {
			got, err := cs.Get(tt.key)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Contains(t, service.ValidConfigKeys(), tt.key)
		})
	}
}

func TestConfigSetWake_GuardOff_Refused(t *testing.T) {
	cs := wakeConfig(t, testutil.OperatorState(t), "", false)
	for _, key := range []string{"wake.max_hop_depth", "wake.backoff_after_unacked"} {
		for _, value := range []string{"0", "-1", "off", "", "1.5"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				_, err := cs.Set(key, value)
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.Contains(t, err.Error(), "always on")
			})
		}
	}
}

func wakeConfig(t *testing.T, state *oplocal.State, agent string, tty bool) *service.ConfigService {
	t.Helper()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	return cs.WithWakeSettings(service.WakeConfigOptions{State: state, Agent: agent, RequireTTY: func() error {
		if !tty {
			return errors.New("terminal stdin required")
		}
		return nil
	}})
}

func TestConfigSetWake_LoosenWithoutTTY_Refused(t *testing.T) {
	state := testutil.OperatorState(t)
	allowed := wakeConfig(t, state, "", true)
	_, err := allowed.Set("wake.wakes_per_hour", "12")
	require.NoError(t, err)
	denied := wakeConfig(t, state, "", false)
	for _, value := range []string{"13", "0", "off", ""} {
		_, err = denied.Set("wake.wakes_per_hour", value)
		require.ErrorContains(t, err, "terminal")
	}
	require.ErrorContains(t, denied.Delete("wake.wakes_per_hour"), "terminal")
	got, err := denied.Get("wake.wakes_per_hour")
	require.NoError(t, err)
	require.Equal(t, "12", got)
	_, err = denied.Set("wake.wakes_per_hour", "11")
	require.NoError(t, err)
}

func TestConfigSetWake_LiveOffWithoutTTY_Allowed(t *testing.T) {
	cs := wakeConfig(t, testutil.OperatorState(t), "", false)
	_, err := cs.Set("wake.live", "off")
	require.NoError(t, err)
	got, err := cs.Get("wake.live")
	require.NoError(t, err)
	require.Equal(t, "false", got)
	_, err = cs.Set("wake.live", "on")
	require.ErrorContains(t, err, "terminal")
	require.ErrorContains(t, cs.Delete("wake.live"), "terminal")
}

func TestConfigSetWake_HostAgentOverridesAndPreservation(t *testing.T) {
	state := testutil.OperatorState(t)
	require.NoError(t, state.WriteJSON("wake", map[string]any{"retained": "unrelated", "max_events": 8}))
	host := wakeConfig(t, state, "", true)
	agent := wakeConfig(t, state, "worker", true)
	_, err := host.Set("wake.wakes_per_hour", "12")
	require.NoError(t, err)
	_, err = agent.Set("wake.wakes_per_hour", "3")
	require.NoError(t, err)
	for _, tt := range []struct {
		svc       *service.ConfigService
		key, want string
	}{
		{host, "wake.wakes_per_hour", "12"}, {agent, "wake.wakes_per_hour", "3"},
		{wakeConfig(t, state, "other", false), "wake.wakes_per_hour", "12"},
		{host, "wake.envelope_max_events", "8"},
	} {
		got, err := tt.svc.Get(tt.key)
		require.NoError(t, err)
		require.Equal(t, tt.want, got)
	}
	_, err = host.Set("wake.live", "off")
	require.NoError(t, err)
	_, err = agent.Set("wake.live", "on")
	require.NoError(t, err)
	got, err := agent.Get("wake.live")
	require.NoError(t, err)
	require.Equal(t, "false", got)
	var raw map[string]any
	_, err = state.ReadJSON("wake", &raw)
	require.NoError(t, err)
	require.Equal(t, "unrelated", raw["retained"])
	require.Contains(t, raw, "host")
	require.Contains(t, raw, "agents")
	require.NoError(t, agent.Delete("wake.wakes_per_hour"))
	got, err = agent.Get("wake.wakes_per_hour")
	require.NoError(t, err)
	require.Equal(t, "12", got)
}

func TestConfigSetWake_CommittedConfigKey_IgnoredAndReported(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("wake:\n  live: off\n  max_hop_depth: 99\ninbox:\n  strict_board_identity: true\n"), 0600))
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)
	cs = cs.WithWakeSettings(service.WakeConfigOptions{State: testutil.OperatorState(t)})
	for _, tt := range []struct{ key, want string }{{"wake.live", "true"}, {"wake.max_hop_depth", "32"}, {"inbox.strict_board_identity", "false"}} {
		got, err := cs.Get(tt.key)
		require.NoError(t, err)
		require.Equal(t, tt.want, got)
	}
	require.ElementsMatch(t, []string{"wake.live", "wake.max_hop_depth", "inbox.strict_board_identity"}, cs.IgnoredWorkspaceKeys())
}

func TestWakeSettings_InvalidScopeAndUnreadableState_Refused(t *testing.T) {
	state := testutil.OperatorState(t)
	for _, agent := range []string{"bad\nagent", string(make([]byte, 65))} {
		cs := wakeConfig(t, state, agent, false)
		_, err := cs.Get("wake.live")
		require.ErrorIs(t, err, model.ErrInvalidInput)
		_, err = cs.Set("wake.live", "off")
		require.ErrorIs(t, err, model.ErrInvalidInput)
	}
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	cs = cs.WithWakeSettings(service.WakeConfigOptions{})
	_, err = cs.Get("wake.live")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	_, err = cs.WakeAgentSettings()
	require.Error(t, err)
	require.NoError(t, state.WriteJSON("wake", map[string]any{"host": map[string]any{"wake.live": "invalid"}}))
	cs = wakeConfig(t, state, "", false)
	_, err = cs.Get("wake.live")
	require.Error(t, err)
	_, err = cs.Set("wake.live", "off")
	require.Error(t, err)
	_, err = cs.WakeAgentSettings()
	require.Error(t, err)
}

func TestWakeSettings_DefaultProcessScopeAndAgentDiagnostics(t *testing.T) {
	state := testutil.OperatorState(t)
	_, err := wakeConfig(t, state, "worker", true).Set("wake.wakes_per_hour", "3")
	require.NoError(t, err)
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	value, err := cs.Get("wake.live")
	require.NoError(t, err)
	require.Equal(t, "true", value)
	_, err = cs.Set("wake.live", "off")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	_, err = wakeConfig(t, state, "", false).Set("wake.live", "off")
	require.NoError(t, err)
	agents, err := cs.WakeAgentSettings()
	require.NoError(t, err)
	require.Equal(t, "3", agents["worker"]["wake.wakes_per_hour"].Value)
	require.Equal(t, "false", agents["worker"]["wake.live"].Value)
}

func TestConfigSetWake_EnvelopeBelowMinimumFraming_Refused(t *testing.T) {
	state := testutil.OperatorState(t)
	cs := wakeConfig(t, state, "", false)
	_, err := cs.Set("wake.envelope_max_total_bytes", "1")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	got, err := cs.Get("wake.envelope_max_total_bytes")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestWakeSettings_UnscopedMutations_RequireCLIContext(t *testing.T) {
	state := testutil.OperatorState(t)
	authorized := wakeConfig(t, state, "", true)
	_, err := authorized.Set("wake.live", "off")
	require.NoError(t, err)
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	for _, tt := range []struct{ key, value string }{
		{"wake.max_hop_depth", "4096"}, {"wake.max_hop_depth", "8"}, {"wake.live", "on"}, {"wake.live", "off"}, {"inbox.strict_board_identity", "true"},
	} {
		_, err = cs.Set(tt.key, tt.value)
		require.ErrorIs(t, err, model.ErrInvalidInput)
		require.ErrorContains(t, err, "mtix config set")
	}
	require.ErrorIs(t, cs.Delete("wake.live"), model.ErrInvalidInput)
	got, err := authorized.Get("wake.live")
	require.NoError(t, err)
	require.Equal(t, "false", got)
}

func TestWakeSettings_InjectedStateWithoutOperatorProof_CannotLoosen(t *testing.T) {
	state := testutil.OperatorState(t)
	_, err := wakeConfig(t, state, "", true).Set("wake.max_hop_depth", "8")
	require.NoError(t, err)
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	cs = cs.WithWakeSettings(service.WakeConfigOptions{State: state})
	_, err = cs.Set("wake.max_hop_depth", "4096")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.ErrorContains(t, err, "operator terminal")
	_, err = cs.Set("wake.max_hop_depth", "4")
	require.NoError(t, err, "tightening needs no operator proof")
}
