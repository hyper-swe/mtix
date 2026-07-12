// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package hooks implements FR-19 event hooks: a project declares subscriptions
// in .mtix/hooks.yaml, and a dispatcher fires matching hooks' delivery adapters
// asynchronously after a mutation commits. This file defines the config schema
// and the normalized Event the matcher runs against; matching lives in
// match.go, loading/validation in load.go.
package hooks

// Adapter names for a hook's `deliver:` list (FR-19.3).
const (
	AdapterInbox      = "inbox"
	AdapterExec       = "exec"
	AdapterWebhook    = "webhook"
	AdapterAppendFile = "append-file"
)

// Canonical hook event names (FR-19.2). A journaled op_type is normalized to
// one of these before matching (see NormalizeEvent).
const (
	EventCommentAddressed = "comment.addressed"
	EventStatusChanged    = "status.changed"
	EventNodeCreated      = "node.created"
)

// Config is the parsed .mtix/hooks.yaml.
type Config struct {
	Hooks []Hook `yaml:"hooks"`
}

// Hook is one subscription: a match predicate plus one or more delivery
// adapters. All Match fields compose with AND (FR-19.2).
type Hook struct {
	Name    string   `yaml:"name"`
	Match   Match    `yaml:"match"`
	Deliver []string `yaml:"deliver"`

	// Per-adapter config (only consulted when the adapter is in Deliver).
	Exec       *ExecConfig       `yaml:"exec,omitempty"`
	Webhook    *WebhookConfig    `yaml:"webhook,omitempty"`
	AppendFile *AppendFileConfig `yaml:"append-file,omitempty"`

	// IncludeSynced is DEPRECATED and a no-op (FR-20): dispatch is
	// origin-independent, so every hook fires on sync-arrived events (deduped
	// per host by the dispatch ledger). The field stays so existing configs
	// parse unchanged; fleet-level "who fires this" is hook placement (§5).
	IncludeSynced bool `yaml:"include-synced,omitempty"`
}

// Match is the AND-composed filter for a hook. Empty fields are wildcards; a
// hook with only `events:` matches broadly.
type Match struct {
	Events       []string `yaml:"events"`
	ToAgent      string   `yaml:"to-agent,omitempty"`
	FromAgentNot string   `yaml:"from-agent-not,omitempty"`
	Under        string   `yaml:"under,omitempty"`
	StatusTo     []string `yaml:"status-to,omitempty"`
}

// ExecConfig configures the exec adapter (FR-19.3). Argv only — never a shell
// string — and a mandatory timeout. Gated by the content-hash trust in 47.5.
type ExecConfig struct {
	Command        []string `yaml:"command"`
	TimeoutSeconds int      `yaml:"timeout-seconds"`
}

// WebhookConfig configures the webhook adapter (FR-19.3).
type WebhookConfig struct {
	URL string `yaml:"url"`
}

// AppendFileConfig configures the append-file adapter (FR-19.3).
type AppendFileConfig struct {
	Path string `yaml:"path"`
}

// Event is the normalized view of a journaled mutation that a hook matches
// against. It is derived from a sync_events row (op_type + payload) by
// NormalizeEvent, so the matcher never touches storage or wire formats.
type Event struct {
	// Seq is the journal rowid — the event's local identity, used by the inbox
	// adapter to record a delivery. Zero for a synthetic event (e.g. a dry-run).
	Seq int64
	// Name is a canonical hook event (EventCommentAddressed, etc.).
	Name string
	// NodeID is the affected node's dot-path id (drives the `under` subtree filter).
	NodeID string
	// Author is the origin agent id (drives from-agent-not + loop prevention).
	Author string
	// ToAgent is the addressee, for comment.addressed (drives to-agent).
	ToAgent string
	// StatusTo is the new status, for status.changed (drives status-to).
	StatusTo string
	// Synced is true when the event arrived via hub replication rather than a
	// local mutation (drives IncludeSynced).
	Synced bool
	// ViaHook names the hook whose exec produced this event, if any — a
	// via-hook event never re-triggers the same hook (loop prevention, 47.7).
	ViaHook string
}
