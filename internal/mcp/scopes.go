// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import "errors"

// ErrReadOnly is wrapped by the error returned when a read-only MCP client
// attempts a write/admin tool (MTIX-2.1.3), so callers/tests can match it.
var ErrReadOnly = errors.New("mcp: read-only client cannot call a mutation tool")

// Tool access scopes (MTIX-2.1.3). Every registered tool is classified so a
// read-only MCP client can be denied mutation tools while still querying.
//
//   - ScopeRead:  pure queries; no state change (safe for a read-only client).
//   - ScopeWrite: ordinary mutations (create/update/claim/comment/…).
//   - ScopeAdmin: destructive or system-level ops (delete/undelete/rerun/docs
//     generation) — a superset of write, reserved for finer future gating.
//
// A read-only client is allowed ScopeRead only; ScopeWrite and ScopeAdmin are
// refused. The classification defaults to ScopeWrite for any unlisted tool, so a
// newly added tool is fail-safe (a read-only client cannot call it until it is
// explicitly marked ScopeRead).
type ToolScope string

const (
	ScopeRead  ToolScope = "read"
	ScopeWrite ToolScope = "write"
	ScopeAdmin ToolScope = "admin"
)

// readScopeTools are the tools that only read state — the allow-list for a
// read-only client. Verified against each handler: none call a mutating service
// method. Keep this the source of truth; everything else is treated as write.
var readScopeTools = map[string]bool{
	"mtix_show":            true,
	"mtix_list":            true,
	"mtix_ready":           true,
	"mtix_blocked":         true,
	"mtix_search":          true,
	"mtix_stale":           true,
	"mtix_orphans":         true,
	"mtix_context":         true,
	"mtix_stats":           true,
	"mtix_discover":        true,
	"mtix_briefing":        true,
	"mtix_progress":        true,
	"mtix_inbox":           true,
	"mtix_inbox_wait":      true,
	"mtix_dep_show":        true,
	"mtix_sync_workflow":   true,
	"mtix_agent_work":      true,
	"mtix_session_summary": true,
}

// adminScopeTools are destructive/system-level mutations. They are a subset of
// non-read tools; the read-only gate treats them the same as write (refused).
// The distinction exists for a future admin-restricted client mode.
var adminScopeTools = map[string]bool{
	"mtix_delete":        true,
	"mtix_undelete":      true,
	"mtix_rerun":         true,
	"mtix_docs_generate": true,
}

// scopeForTool classifies a tool by name: read if allow-listed, admin if in the
// destructive set, otherwise write (the fail-safe default).
func scopeForTool(name string) ToolScope {
	switch {
	case readScopeTools[name]:
		return ScopeRead
	case adminScopeTools[name]:
		return ScopeAdmin
	default:
		return ScopeWrite
	}
}
