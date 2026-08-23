// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/segment"
)

// validConfigKeys lists all 36 allowed config keys per FR-11.2.
//
// The sync.relay.* family configures the FR-21 file transport. It is a
// second transport beside the hub, so its cadence and thresholds are
// its own settings rather than the hub's: the daemon's tick and a
// shared medium's propagation are unrelated concerns (D-R12).
var validConfigKeys = map[string]bool{
	"prefix":                       true,
	"author_id":                    true,
	"mcp.read_only":                true,
	"api.bind":                     true,
	"api.http_port":                true,
	"api.grpc_port":                true,
	"api.rate_limit":               true,
	"mcp.enabled":                  true,
	"mcp.transport":                true,
	"data.dir":                     true,
	"data.soft_delete_retention":   true,
	"sync.enabled":                 true,
	"sync.endpoint":                true,
	"sync.team_id":                 true,
	"sync.auto_sync":               true,
	"sync.interval":                true,
	"sync.relay.dir":               true,
	"sync.relay.peer_id":           true,
	"sync.relay.poll_interval":     true,
	"sync.relay.retention_days":    true,
	"sync.relay.silent_peer_days":  true,
	"sync.relay.max_segment_bytes": true,
	"sync.relay.require_auth":      true,
	"agent.heartbeat_interval":     true,
	"agent.stale_threshold":        true,
	"agent.session_timeout":        true,
	"agent.stuck_timeout":          true,
	"agent.id_pattern":             true,
	"agent.auto_claim":             true,
	"context.token_estimator":      true,
	"logging.file":                 true,
	"logging.level":                true,
	"progress.weighted":            true,
	"ui.default_depth":             true,
	"ui.collapse_done":             true,
	"ui.theme":                     true,
}

// valueValidators check a value before it is stored, for keys where a
// malformed one fails somewhere far from the command that set it.
//
// The check belongs here so an operator learns at the moment they type
// it rather than at the next tick, when the message would arrive
// detached from the action that caused it.
var valueValidators = map[string]func(string) error{
	// FR-21 §5.2 identity grammar. Empty means "derive from this
	// machine", which is the default. The grammar is reused from the
	// relay's own frame layer rather than restated — a second copy of
	// an identity rule is a second thing to disagree.
	"sync.relay.peer_id": func(v string) error {
		if v == "" {
			return nil
		}
		if err := segment.ValidatePeerID(v); err != nil {
			return fmt.Errorf("sync.relay.peer_id %q is malformed "+
				"(16 hex characters, optionally -label): %w", v, model.ErrInvalidInput)
		}
		return nil
	},
}

// serverRestartKeys are keys that affect a running server and require restart.
var serverRestartKeys = map[string]bool{
	"api.bind":       true,
	"api.http_port":  true,
	"api.grpc_port":  true,
	"api.rate_limit": true,
	"mcp.enabled":    true,
	"mcp.transport":  true,
	"logging.file":   true,
	"logging.level":  true,
}

// configDefaults contains default values for all 36 keys per FR-11.2.
var configDefaults = map[string]string{
	"prefix":                     "PROJ",
	"author_id":                  "",
	"mcp.read_only":              "false",
	"api.bind":                   "127.0.0.1",
	"api.http_port":              "6849",
	"api.grpc_port":              "6850",
	"api.rate_limit":             "100",
	"mcp.enabled":                "true",
	"mcp.transport":              "stdio",
	"data.dir":                   ".mtix/data",
	"data.soft_delete_retention": "30d",
	"sync.enabled":               "false",
	"sync.endpoint":              "",
	"sync.team_id":               "",
	"sync.auto_sync":             "true",
	"sync.interval":              "30s",
	// An empty relay directory means no relay is configured; every
	// relay phase is then a no-op rather than an error.
	"sync.relay.dir": "",
	// An operator-assigned relay identity, for peers whose workspace is
	// rebuilt somewhere else between sessions. Empty derives it from
	// the machine, which is right whenever the machine is the thing
	// that persists.
	"sync.relay.peer_id": "",
	// Poll cadence is the relay's own, not the daemon's (D-R12). "0"
	// is legal and declares a tick-only peer (§6.6) — a turn-driven
	// agent seat, a cron-locked appliance, a sneakernet courier — which
	// is a deployment, not a misconfiguration.
	"sync.relay.poll_interval": "5s",
	// The prune safety window, and the silence threshold that prompts
	// retirement at twice it (§6.7).
	"sync.relay.retention_days":   "7",
	"sync.relay.silent_peer_days": "14",
	// Rotation threshold (§5.3).
	"sync.relay.max_segment_bytes": "4194304",
	// Authenticated by default: the relay medium is a less trusted
	// channel than the hub, so running without MACs is an explicit,
	// logged opt-out rather than something a peer drifts into (§8.3).
	"sync.relay.require_auth":  "true",
	"agent.heartbeat_interval": "60s",
	"agent.stale_threshold":    "24h",
	"agent.session_timeout":    "4h",
	"agent.stuck_timeout":      "",
	"agent.id_pattern":         "agent-*",
	"agent.auto_claim":         "true",
	"context.token_estimator":  "chars4",
	"logging.file":             ".mtix/logs/mtix.log",
	"logging.level":            "info",
	"progress.weighted":        "false",
	"ui.default_depth":         "3",
	"ui.collapse_done":         "false",
	"ui.theme":                 "system",
}

// ConfigService manages mtix configuration per FR-11.1 and FR-11.2.
// Stores config in .mtix/config.yaml with dot-notation key access.
type ConfigService struct {
	values map[string]string
	path   string // Path to config file.
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

// Get returns the value for a config key using dot-notation.
// Returns ErrInvalidConfigKey if the key is not recognized.
func (cs *ConfigService) Get(key string) (string, error) {
	if !validConfigKeys[key] {
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

// Set writes a config value for the given key.
// Returns ErrInvalidConfigKey if the key is not recognized.
// Returns a warning string if the key requires server restart.
func (cs *ConfigService) Set(key, value string) (string, error) {
	if !validConfigKeys[key] {
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

	cs.values[key] = value

	if err := cs.saveToFile(); err != nil {
		return "", fmt.Errorf("save config: %w", err)
	}

	if serverRestartKeys[key] {
		return "Server restart required for this change to take effect.", nil
	}
	return "", nil
}

// Delete removes a config key, reverting it to its default value.
// Returns ErrInvalidConfigKey if the key is not recognized.
func (cs *ConfigService) Delete(key string) error {
	if !validConfigKeys[key] {
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

		if validConfigKeys[fullKey] {
			cs.values[fullKey] = value
		}
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
		"agent", "context", "logging", "progress", "ui",
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

	return os.WriteFile(cs.path, []byte(buf.String()), 0o644)
}

// parseDuration parses a duration string with support for "d" (days) suffix.
func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	// Handle "30d" format.
	if strings.HasSuffix(s, "d") {
		s = strings.TrimSuffix(s, "d")
		days := 0
		for _, c := range s {
			if c < '0' || c > '9' {
				return fallback
			}
			days = days*10 + int(c-'0')
		}
		return time.Duration(days) * 24 * time.Hour
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

// ValidConfigKeys returns all valid config key names for documentation introspection.
// Used by DocGen engine per FR-13.2.
func ValidConfigKeys() []string {
	keys := make([]string, 0, len(validConfigKeys))
	for k := range validConfigKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validKeyList returns a comma-separated list of valid config keys.
func validKeyList() string {
	keys := make([]string, 0, len(validConfigKeys))
	for k := range validConfigKeys {
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
