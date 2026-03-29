// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestConfig_Load_ValidYAML verifies config loads from file.
func TestConfig_Load_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := `prefix: "MYPROJ"

api:
  http_port: 8080
  bind: "0.0.0.0"

agent:
  auto_claim: false
`
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))

	cs, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	v, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "MYPROJ", v)

	v, err = cs.Get("api.http_port")
	require.NoError(t, err)
	assert.Equal(t, "8080", v)
}

// TestConfig_Get_ExistingKey_ReturnsValue verifies Get returns set values.
func TestConfig_Get_ExistingKey_ReturnsValue(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	// Should return default value.
	v, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "PROJ", v)
}

// TestConfig_Get_NestedKey_DotNotation verifies dot-notation access.
func TestConfig_Get_NestedKey_DotNotation(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	v, err := cs.Get("api.http_port")
	require.NoError(t, err)
	assert.Equal(t, "6849", v)

	v, err = cs.Get("agent.stale_threshold")
	require.NoError(t, err)
	assert.Equal(t, "24h", v)
}

// TestConfig_Set_ValidKey_WritesToFile verifies Set persists to file.
func TestConfig_Set_ValidKey_WritesToFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mtix", "config.yaml")

	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	// Set path by initializing.
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	warning, err := cs.Set("api.http_port", "9999")
	require.NoError(t, err)
	assert.Contains(t, warning, "restart") // api.* keys require restart.

	// Verify persisted by re-loading.
	cs2, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	v, err := cs2.Get("api.http_port")
	require.NoError(t, err)
	assert.Equal(t, "9999", v)
}

// TestConfig_Set_InvalidKey_ReturnsErrInvalidConfigKey verifies key validation.
func TestConfig_Set_InvalidKey_ReturnsErrInvalidConfigKey(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	_, err = cs.Set("nonexistent.key", "value")
	assert.ErrorIs(t, err, model.ErrInvalidConfigKey)
}

// TestConfig_Set_ServerKey_ReturnsRestartWarning verifies restart warnings.
func TestConfig_Set_ServerKey_ReturnsRestartWarning(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	serverKeys := []string{
		"api.bind", "api.http_port", "api.grpc_port",
		"mcp.enabled", "mcp.transport",
		"logging.file", "logging.level",
	}

	for _, key := range serverKeys {
		t.Run(key, func(t *testing.T) {
			warning, err := cs.Set(key, "test-value")
			require.NoError(t, err)
			assert.Contains(t, warning, "restart",
				"key %s should warn about restart", key)
		})
	}
}

// TestConfig_Delete_RemovesKey verifies delete reverts to default.
func TestConfig_Delete_RemovesKey(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	// Set a non-default value.
	_, err = cs.Set("prefix", "CUSTOM")
	require.NoError(t, err)

	v, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "CUSTOM", v)

	// Delete — should revert to default.
	err = cs.Delete("prefix")
	require.NoError(t, err)

	v, err = cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "PROJ", v) // Default value.
}

// TestConfig_DefaultValues_AllPresent verifies all 27 keys have defaults.
func TestConfig_DefaultValues_AllPresent(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	expectedKeys := []string{
		"prefix",
		"api.bind", "api.http_port", "api.grpc_port", "api.rate_limit",
		"mcp.enabled", "mcp.transport",
		"data.dir", "data.soft_delete_retention",
		"sync.enabled", "sync.endpoint", "sync.team_id", "sync.auto_sync", "sync.interval",
		"agent.heartbeat_interval", "agent.stale_threshold", "agent.session_timeout",
		"agent.stuck_timeout", "agent.id_pattern", "agent.auto_claim",
		"context.token_estimator",
		"logging.file", "logging.level",
		"progress.weighted",
		"ui.default_depth", "ui.collapse_done", "ui.theme",
	}

	assert.Len(t, expectedKeys, 27, "should have exactly 27 config keys")

	for _, key := range expectedKeys {
		_, err := cs.Get(key)
		assert.NoError(t, err, "key %s should be valid", key)
	}
}

// TestConfig_InitConfig_CreatesFile verifies init creates directory structure.
func TestConfig_InitConfig_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	err = cs.InitConfig(dir, "MYPROJ")
	require.NoError(t, err)

	// Verify directories exist.
	assert.DirExists(t, filepath.Join(dir, ".mtix"))
	assert.DirExists(t, filepath.Join(dir, ".mtix", "data"))
	assert.DirExists(t, filepath.Join(dir, ".mtix", "logs"))

	// Verify config file exists.
	configPath := filepath.Join(dir, ".mtix", "config.yaml")
	assert.FileExists(t, configPath)

	// Verify prefix is set.
	cs2, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	v, err := cs2.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "MYPROJ", v)
}

// TestConfig_ConfigProvider_Interface verifies ConfigProvider implementation.
func TestConfig_ConfigProvider_Interface(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	// Verify ConfigService implements ConfigProvider.
	var _ service.ConfigProvider = cs

	assert.True(t, cs.AutoClaim()) // Default is true.
	assert.Equal(t, 30*24, int(cs.SoftDeleteRetention().Hours()))
	assert.Equal(t, 24, int(cs.AgentStaleThreshold().Hours()))
	assert.Equal(t, 50, cs.MaxRecommendedDepth())
}

// TestConfig_AgentStuckTimeout_DefaultsToZero verifies FR-10.3a default (no auto-unclaim).
func TestConfig_AgentStuckTimeout_DefaultsToZero(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	timeout := cs.AgentStuckTimeout()
	assert.Equal(t, time.Duration(0), timeout,
		"default stuck timeout should be 0 (disabled)")
}

// TestConfig_AgentStuckTimeout_WithCustomValue verifies custom stuck timeout.
func TestConfig_AgentStuckTimeout_WithCustomValue(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	_, err = cs.Set("agent.stuck_timeout", "30m")
	require.NoError(t, err)

	timeout := cs.AgentStuckTimeout()
	assert.Equal(t, 30*time.Minute, timeout)
}

// TestConfig_SessionTimeout_DefaultsFourHours verifies FR-10.5a default.
func TestConfig_SessionTimeout_DefaultsFourHours(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	timeout := cs.SessionTimeout()
	assert.Equal(t, 4*time.Hour, timeout)
}

// TestConfig_SessionTimeout_WithCustomValue verifies custom session timeout.
func TestConfig_SessionTimeout_WithCustomValue(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	_, err = cs.Set("agent.session_timeout", "8h")
	require.NoError(t, err)

	timeout := cs.SessionTimeout()
	assert.Equal(t, 8*time.Hour, timeout)
}

// TestConfig_ValidConfigKeys_Returns27Keys verifies FR-13.2 documentation introspection.
func TestConfig_ValidConfigKeys_Returns27Keys(t *testing.T) {
	keys := service.ValidConfigKeys()

	assert.Len(t, keys, 27, "should return exactly 27 valid config keys")

	// Verify sorted order.
	for i := 1; i < len(keys); i++ {
		assert.True(t, keys[i-1] < keys[i],
			"keys should be sorted: %s should come before %s", keys[i-1], keys[i])
	}

	// Verify some known keys are present.
	assert.Contains(t, keys, "prefix")
	assert.Contains(t, keys, "api.bind")
	assert.Contains(t, keys, "agent.stuck_timeout")
	assert.Contains(t, keys, "ui.theme")
}

// TestConfig_Get_InvalidKey_ReturnsErrInvalidConfigKey verifies key validation.
func TestConfig_Get_InvalidKey_ReturnsErrInvalidConfigKey(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	_, err = cs.Get("nonexistent.key")
	assert.ErrorIs(t, err, model.ErrInvalidConfigKey)
}

// TestConfig_Delete_InvalidKey_ReturnsErrInvalidConfigKey verifies key validation.
func TestConfig_Delete_InvalidKey_ReturnsErrInvalidConfigKey(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	err = cs.Delete("nonexistent.key")
	assert.ErrorIs(t, err, model.ErrInvalidConfigKey)
}

// TestConfig_Set_NonServerKey_ReturnsEmptyWarning verifies no restart warning.
func TestConfig_Set_NonServerKey_ReturnsEmptyWarning(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	warning, err := cs.Set("prefix", "NEWPROJ")
	require.NoError(t, err)
	assert.Empty(t, warning, "non-server key should not produce restart warning")
}

// TestConfig_NewConfigService_InvalidFile_ReturnsError verifies bad file handling.
func TestConfig_NewConfigService_InvalidFile_ReturnsError(t *testing.T) {
	// Create a directory where a file is expected — causes read error.
	dir := t.TempDir()
	dirAsFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.MkdirAll(dirAsFile, 0o755))

	_, err := service.NewConfigService(dirAsFile)
	assert.Error(t, err)
}

// TestConfig_ParseDuration_DaySuffix verifies "d" parsing in retention config.
func TestConfig_ParseDuration_DaySuffix(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	_, err = cs.Set("data.soft_delete_retention", "7d")
	require.NoError(t, err)

	retention := cs.SoftDeleteRetention()
	assert.Equal(t, 7*24*time.Hour, retention)
}

// TestConfig_ParseDuration_DaySuffix_NonNumericChars verifies invalid day format.
func TestConfig_ParseDuration_DaySuffix_NonNumericChars(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	// "abcd" is not a valid day-format duration.
	_, err = cs.Set("data.soft_delete_retention", "abcd")
	require.NoError(t, err)

	retention := cs.SoftDeleteRetention()
	// Falls back to default 30 days.
	assert.Equal(t, 30*24*time.Hour, retention)
}

// TestConfig_Get_KeyInValuesMap_ReturnsStoredValue verifies in-memory value lookup.
func TestConfig_Get_KeyInValuesMap_ReturnsStoredValue(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	// Set and then get to hit the values[key] path.
	_, err = cs.Set("prefix", "CUSTOM")
	require.NoError(t, err)

	v, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "CUSTOM", v)
}

// TestConfig_LoadFromFile_InlineComments verifies inline comment stripping.
func TestConfig_LoadFromFile_InlineComments(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := `prefix: MYPROJ # This is a comment
api:
  http_port: 8080 # HTTP port
`
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))

	cs, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	v, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "MYPROJ", v)

	v, err = cs.Get("api.http_port")
	require.NoError(t, err)
	assert.Equal(t, "8080", v)
}

// TestConfig_LoadFromFile_QuotedValues verifies quote stripping.
func TestConfig_LoadFromFile_QuotedValues(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := `prefix: 'QUOTED'
api:
  bind: "0.0.0.0"
`
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))

	cs, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	v, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "QUOTED", v)

	v, err = cs.Get("api.bind")
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0", v)
}

// TestConfig_LoadFromFile_IgnoresUnknownKeys verifies unknown keys are skipped.
func TestConfig_LoadFromFile_IgnoresUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := `prefix: PROJ
unknown_key: should_be_ignored
api:
  http_port: 9999
`
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))

	cs, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	// Valid key should load.
	v, err := cs.Get("api.http_port")
	require.NoError(t, err)
	assert.Equal(t, "9999", v)
}

// TestConfig_ParseDuration_DaySuffix_WithNonDigitChars verifies "1x2d" fallback.
func TestConfig_ParseDuration_DaySuffix_WithNonDigitChars(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	// "1x2d" has non-digit chars before "d" suffix — should fallback.
	_, err = cs.Set("data.soft_delete_retention", "1x2d")
	require.NoError(t, err)

	retention := cs.SoftDeleteRetention()
	assert.Equal(t, 30*24*time.Hour, retention, "should fall back to default for invalid day format")
}

// TestConfig_Get_DefaultValue_NotInValuesMap verifies default fallback.
func TestConfig_Get_DefaultValue_NotInValuesMap(t *testing.T) {
	// Create a config service with empty file — values will have defaults.
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	// "sync.endpoint" defaults to empty string.
	v, err := cs.Get("sync.endpoint")
	require.NoError(t, err)
	assert.Equal(t, "", v)
}

// TestConfig_Delete_PersistsToFile verifies delete writes to disk.
func TestConfig_Delete_PersistsToFile(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	// Set, then delete.
	_, err = cs.Set("ui.theme", "dark")
	require.NoError(t, err)

	err = cs.Delete("ui.theme")
	require.NoError(t, err)

	// Reload and verify default.
	configPath := filepath.Join(dir, ".mtix", "config.yaml")
	cs2, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	v, err := cs2.Get("ui.theme")
	require.NoError(t, err)
	assert.Equal(t, "system", v, "should revert to default after delete")
}

// TestConfig_LoadFromFile_EmptyLines_Skipped verifies empty line handling.
func TestConfig_LoadFromFile_EmptyLines_Skipped(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := `
# Comment line

prefix: TEST

# Another comment
api:

  http_port: 7777

`
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))

	cs, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	v, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "TEST", v)

	v, err = cs.Get("api.http_port")
	require.NoError(t, err)
	assert.Equal(t, "7777", v)
}

// TestConfig_ParseDuration_InvalidString_UsesFallback verifies fallback behavior.
func TestConfig_ParseDuration_InvalidString_UsesFallback(t *testing.T) {
	dir := t.TempDir()
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	require.NoError(t, cs.InitConfig(dir, "PROJ"))

	_, err = cs.Set("data.soft_delete_retention", "not-a-duration")
	require.NoError(t, err)

	retention := cs.SoftDeleteRetention()
	// Should fall back to default 30 days.
	assert.Equal(t, 30*24*time.Hour, retention)
}
