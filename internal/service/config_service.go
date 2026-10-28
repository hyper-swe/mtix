// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// ConfigService manages workspace configuration (FR-11.1/11.2) and host wake
// settings (FR-20.3/20.5). Wake authority is stored operator-locally; ordinary
// workspace values use .mtix/config.yaml with dot-notation key access.
type ConfigService struct {
	wake        *WakeConfigOptions
	reevaluator WakeReevaluator
	ignoredWake map[string]string
	values      map[string]string
	path        string // Path to config file.
	// historyGuard checks the history.export rollout guard (MTIX-95.31.19.6).
	historyGuard func() (HistoryGuardReport, error)
}

// NewConfigService creates a ConfigService, loading from the given path.
// If the file doesn't exist, returns a service with defaults only.
func NewConfigService(configPath string) (*ConfigService, error) {
	cs := &ConfigService{
		values: make(map[string]string),
		path:   configPath,
	}

	// Start with defaults.
	for k, v := range configDefaults {
		cs.values[k] = v
	}

	// Load from file if it exists.
	if configPath != "" {
		if err := cs.loadFromFile(configPath); err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("load config: %w", err)
			}
		}
	}

	return cs, nil
}

// Get returns a workspace or effective host/agent value using dot-notation.
// Returns ErrInvalidConfigKey if the key is not recognized.
func (cs *ConfigService) Get(key string) (string, error) {
	if model.IsWakeSetting(key) {
		return cs.getWakeSetting(key)
	}
	if !validConfigKey(key) {
		return "", fmt.Errorf(
			"unknown config key %q; valid keys: %s: %w",
			key, validKeyList(), model.ErrInvalidConfigKey,
		)
	}

	if v, ok := cs.values[key]; ok {
		return v, nil
	}
	return configDefaults[key], nil
}

// Set writes a config value for the given key. Host wake changes use protected
// operator-local storage; loosening requires terminal stdin (FR-20.3).
// Returns ErrInvalidConfigKey if the key is not recognized, and
// ErrInvalidInput for a sync.auto_sync value that is neither true nor false
// (MTIX-95.31.2), a sync.max_lamport_jump value that is not a positive
// integer (MTIX-95.11), or a sync.keep_roles value that is not a list of
// roles that may be kept (model.ParseKeepRoles, MTIX-95.1); nothing is
// written then.
// Returns a warning string if the key requires server restart.
func (cs *ConfigService) Set(key, value string) (string, error) {
	if model.IsWakeSetting(key) {
		return cs.setWakeSetting(key, value)
	}
	if !validConfigKey(key) {
		return "", fmt.Errorf(
			"unknown config key %q; valid keys: %s: %w",
			key, validKeyList(), model.ErrInvalidConfigKey,
		)
	}
	if validate := valueValidators[key]; validate != nil {
		if err := validate(value); err != nil {
			return "", err
		}
	}
	if err := validateMaxLamportJump(key, value); err != nil {
		return "", err
	}
	if err := cs.guardHistoryExport(key, value); err != nil {
		return "", err
	}
	if key == "sync.keep_roles" {
		if _, err := model.ParseKeepRoles(value); err != nil {
			return "", fmt.Errorf("set %s: %w", key, err)
		}
	}

	cs.values[key] = value

	if err := cs.saveToFile(); err != nil {
		return "", fmt.Errorf("save config: %w", err)
	}

	if serverRestartKeys[key] {
		return "Server restart required for this change to take effect.", nil
	}
	return "", nil
}

// Delete removes an override, reverting to its inherited or default value.
// Loosening host wake policy requires terminal stdin (FR-20.3).
// Returns ErrInvalidConfigKey if the key is not recognized.
func (cs *ConfigService) Delete(key string) error {
	if model.IsWakeSetting(key) {
		return cs.changeWakeSetting(key, nil)
	}
	if !validConfigKey(key) {
		return fmt.Errorf(
			"unknown config key %q; valid keys: %s: %w",
			key, validKeyList(), model.ErrInvalidConfigKey,
		)
	}

	cs.values[key] = configDefaults[key]

	if err := cs.saveToFile(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// AutoClaim implements ConfigProvider.AutoClaim per FR-11.2a.
func (cs *ConfigService) AutoClaim() bool {
	v, _ := cs.Get("agent.auto_claim")
	return v == "true"
}

// AutoSyncSetting reports the sync.auto_sync switch (default true;
// FR-15.2j, MTIX-95.31.2): the value as configured, and whether a changed
// .mtix/tasks.json is imported automatically. A value that is neither true
// nor false (strconv.ParseBool, surrounding spaces ignored) returns an
// error, and auto-import is then on, the default. Auto-export does not
// depend on it.
func (cs *ConfigService) AutoSyncSetting() (raw string, on bool, err error) {
	raw, _ = cs.Get("sync.auto_sync")
	on, err = parseAutoSync(raw)
	if err != nil {
		return raw, true, err
	}
	return raw, on, nil
}

// parseAutoSync reads a sync.auto_sync value: true or false in any form
// strconv.ParseBool accepts (MTIX-95.31.2).
func parseAutoSync(value string) (bool, error) {
	on, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("sync.auto_sync must be true or false, not %q: %w", value, model.ErrInvalidInput)
	}
	return on, nil
}

// AuthorID returns the configured default author identity for emitted sync
// events (MTIX-24), or "" when unset. The MTIX_AUTHOR_ID env overrides this
// per-process; see resolveEmitAuthor in the sqlite store.
func (cs *ConfigService) AuthorID() string {
	v, _ := cs.Get("author_id")
	return v
}

// MCPReadOnly reports whether the MCP server should run in read-only mode
// (MTIX-2.1.3), refusing mutation tools. The `mtix mcp --read-only` flag
// overrides this per invocation.
func (cs *ConfigService) MCPReadOnly() bool {
	v, _ := cs.Get("mcp.read_only")
	return v == "true"
}

// SoftDeleteRetention implements ConfigProvider.SoftDeleteRetention per FR-3.3.
func (cs *ConfigService) SoftDeleteRetention() time.Duration {
	v, _ := cs.Get("data.soft_delete_retention")
	return parseDuration(v, 30*24*time.Hour)
}

// AgentStaleThreshold implements ConfigProvider.AgentStaleThreshold per FR-10.4a.
func (cs *ConfigService) AgentStaleThreshold() time.Duration {
	v, _ := cs.Get("agent.stale_threshold")
	return parseDuration(v, 24*time.Hour)
}

// MaxRecommendedDepth implements ConfigProvider.MaxRecommendedDepth per FR-1.1a.
func (cs *ConfigService) MaxRecommendedDepth() int {
	return 50 // Hardcoded per spec.
}

// AgentStuckTimeout implements ConfigProvider.AgentStuckTimeout per FR-10.3a.
func (cs *ConfigService) AgentStuckTimeout() time.Duration {
	v, _ := cs.Get("agent.stuck_timeout")
	return parseDuration(v, 0) // Default: 0 (no auto-unclaim).
}

// SessionTimeout implements ConfigProvider.SessionTimeout per FR-10.5a.
func (cs *ConfigService) SessionTimeout() time.Duration {
	v, _ := cs.Get("agent.session_timeout")
	return parseDuration(v, 4*time.Hour)
}

// InitConfig creates the initial .mtix/ directory structure and config file.
// Called by `mtix init`.
func (cs *ConfigService) InitConfig(rootDir, prefix string) error {
	mtixDir := filepath.Join(rootDir, ".mtix")
	dirs := []string{
		mtixDir,
		filepath.Join(mtixDir, "data"),
		filepath.Join(mtixDir, "logs"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	cs.path = filepath.Join(mtixDir, "config.yaml")
	cs.values["prefix"] = prefix

	return cs.saveToFile()
}

// loadFromFile parses a simple YAML config file.
// Uses a minimal parser since viper import is deferred to CLI layer.
func (cs *ConfigService) loadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Simple YAML parser for flat and one-level nested keys.
	lines := strings.Split(string(data), "\n")
	var currentSection string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Check if this is a section header (no value, ends with ':')
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") &&
			strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " ") {
			currentSection = strings.TrimSuffix(trimmed, ":")
			continue
		}

		// Parse key: value
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove inline comments.
		if idx := strings.Index(value, " #"); idx >= 0 {
			value = strings.TrimSpace(value[:idx])
		}

		// Remove surrounding quotes.
		value = strings.Trim(value, "\"'")

		// Build full key with section prefix.
		fullKey := key
		if currentSection != "" && strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			fullKey = currentSection + "." + key
		}

		cs.loadWorkspaceKey(fullKey, value)
	}

	return nil
}

// saveToFile writes the current config to the YAML file.
func (cs *ConfigService) saveToFile() error {
	if cs.path == "" {
		return nil // No file path configured.
	}

	dir := filepath.Dir(cs.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	var buf strings.Builder
	sections := []string{
		"", "api", "mcp", "data", "sync",
		"agent", "context", "logging", "progress", "ui", "history",
	}

	for _, section := range sections {
		if section != "" {
			buf.WriteString("\n" + section + ":\n")
		}
		for key, value := range cs.values {
			if section == "" {
				if !strings.Contains(key, ".") {
					buf.WriteString(key + ": " + quoteIfNeeded(value) + "\n")
				}
			} else if strings.HasPrefix(key, section+".") {
				subKey := strings.TrimPrefix(key, section+".")
				buf.WriteString("  " + subKey + ": " + quoteIfNeeded(value) + "\n")
			}
		}
	}

	return writeFileAtomic(cs.path, []byte(buf.String()))
}

// ValidConfigKeys returns all valid config key names for documentation introspection.
// Used by DocGen engine per FR-13.2.
func ValidConfigKeys() []string {
	keys := make([]string, 0, len(validConfigKeys)+len(model.WakeSettingDefaults()))
	for k := range validConfigKeys {
		keys = append(keys, k)
	}
	for k := range model.WakeSettingDefaults() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validKeyList returns a comma-separated list of valid config keys.
func validKeyList() string {
	keys := make([]string, 0, len(validConfigKeys)+len(model.WakeSettingDefaults()))
	for k := range validConfigKeys {
		keys = append(keys, k)
	}
	for k := range model.WakeSettingDefaults() {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

// quoteIfNeeded wraps a value in quotes if it contains special YAML characters.
func quoteIfNeeded(s string) string {
	if s == "" || s == "true" || s == "false" {
		return s
	}
	// Check if it's a number.
	isNum := true
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' && c != '-' {
			isNum = false
			break
		}
	}
	if isNum {
		return s
	}
	// Quote strings that might be misinterpreted.
	if strings.ContainsAny(s, ":{}[]!@#$%^&*|\\") {
		return "\"" + s + "\""
	}
	return "\"" + s + "\""
}

func (cs *ConfigService) loadWorkspaceKey(key, value string) {
	if ignoredWorkspaceKey(key) {
		if cs.ignoredWake == nil {
			cs.ignoredWake = map[string]string{}
		}
		cs.ignoredWake[key] = value
		return
	}
	if validConfigKeys[key] {
		cs.values[key] = value
	}
}
