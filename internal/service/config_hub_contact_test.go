// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func newHubContactConfig(t *testing.T) (*service.ConfigService, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)
	return cs, path
}

func TestConfigKeys_HubContactValidated(t *testing.T) {
	cs, _ := newHubContactConfig(t)
	tests := []struct {
		key, value string
		wantErr    bool
	}{
		{"sync.hub_contact", "activity", false},
		{"sync.hub_contact", "manual", false},
		{"sync.hub_contact", "interval:30s", false},
		{"sync.hub_contact", "interval:0s", true},
		{"sync.hub_contact", "sometimes", true},
		{"sync.push_on_addressed_send", "true", false},
		{"sync.push_on_addressed_send", "false", false},
		{"sync.push_on_addressed_send", "maybe", true},
		{"sync.listen", "live", false},
		{"sync.listen", "off", false},
		{"sync.listen", "on", true},
		{"sync.listen_interval", "10s", false},
		{"sync.listen_interval", "0s", true},
		{"sync.listen_max_idle", "8h", false},
		{"sync.listen_max_idle", "999h", true},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			_, err := cs.Set(tt.key, tt.value)
			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				return
			}
			require.NoError(t, err)
			got, getErr := cs.Get(tt.key)
			require.NoError(t, getErr)
			require.Equal(t, tt.value, got)
		})
	}
}

func TestConfigKeys_HubContactDefaults(t *testing.T) {
	cs, _ := newHubContactConfig(t)
	want := map[string]string{
		"sync.hub_contact":            "activity",
		"sync.push_on_addressed_send": "true",
		"sync.listen_interval":        "10s",
		"sync.listen_max_idle":        "8h",
	}
	for k, v := range want {
		got, err := cs.Get(k)
		require.NoError(t, err)
		require.Equal(t, v, got, k)
	}
	listen, err := cs.ListenSettings()
	require.NoError(t, err)
	require.Equal(t, service.DefaultListenHold, listen.Hold, "sync.listen default follows the one constant")
	require.Equal(t, 10*time.Second, listen.Interval)
	require.Equal(t, 8*time.Hour, listen.MaxIdle)
}

func TestConfigHubContact_LegacyConfigWithoutKey_IsGrandfathered(t *testing.T) {
	_, path := newHubContactConfig(t)
	require.NoError(t, os.WriteFile(path, []byte("prefix: \"X\"\nsync:\n  enabled: \"true\"\n"), 0o600))
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)

	mode, grandfathered, err := cs.HubContact(0)
	require.NoError(t, err)
	require.True(t, grandfathered)
	require.Equal(t, service.HubContactInterval, mode.Kind)
	require.Equal(t, 5*time.Second, mode.Interval)

	// An unrelated Set must not silently end the grandfathering.
	_, err = cs.Set("sync.interval", "45s")
	require.NoError(t, err)
	again, err := service.NewConfigService(path)
	require.NoError(t, err)
	_, still, err := again.HubContact(0)
	require.NoError(t, err)
	require.True(t, still)
}

func TestConfigHubContact_ExplicitSet_EndsGrandfathering(t *testing.T) {
	_, path := newHubContactConfig(t)
	require.NoError(t, os.WriteFile(path, []byte("prefix: \"X\"\n"), 0o600))
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)
	_, err = cs.Set("sync.hub_contact", "activity")
	require.NoError(t, err)

	again, err := service.NewConfigService(path)
	require.NoError(t, err)
	mode, grandfathered, err := again.HubContact(0)
	require.NoError(t, err)
	require.False(t, grandfathered)
	require.Equal(t, service.HubContactActivity, mode.Kind)
}

func TestConfigHubContact_NewBoard_IsActivity(t *testing.T) {
	cs, _ := newHubContactConfig(t)
	root := t.TempDir()
	require.NoError(t, cs.InitConfig(root, "NEW"))
	fresh, err := service.NewConfigService(filepath.Join(root, ".mtix", "config.yaml"))
	require.NoError(t, err)
	mode, grandfathered, err := fresh.HubContact(0)
	require.NoError(t, err)
	require.False(t, grandfathered)
	require.Equal(t, service.HubContactActivity, mode.Kind)
}

func TestConfigHubContact_Grandfathered_KeepsDaemonInterval(t *testing.T) {
	_, path := newHubContactConfig(t)
	require.NoError(t, os.WriteFile(path, []byte("prefix: \"X\"\n"), 0o600))
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)
	mode, grandfathered, err := cs.HubContact(60 * time.Second)
	require.NoError(t, err)
	require.True(t, grandfathered)
	require.Equal(t, 60*time.Second, mode.Interval, "an upgrade must not raise the contact rate")
}

func TestConfigGet_Grandfathered_ReportsEffectiveMode(t *testing.T) {
	_, path := newHubContactConfig(t)
	require.NoError(t, os.WriteFile(path, []byte("prefix: \"X\"\n"), 0o600))
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)
	got, err := cs.Get("sync.hub_contact")
	require.NoError(t, err)
	_, parseErr := service.ParseHubContact(got)
	require.NoError(t, parseErr, "a grandfathered get returns a parseable value")
	require.Equal(t, "interval:5s", got, "a grandfathered board reports its effective interval mode, not the activity default")
	require.True(t, cs.HubContactGrandfathered())

	_, err = cs.Set("sync.hub_contact", "manual")
	require.NoError(t, err)
	got, err = cs.Get("sync.hub_contact")
	require.NoError(t, err)
	require.Equal(t, "manual", got)
	require.False(t, cs.HubContactGrandfathered())
}
