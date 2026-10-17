// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRegisterAllTools_ZeroToolSet_RegistersEveryCategory: the shared
// registration path covers each tool category, with no project services
// (MTIX-107.42).
func TestRegisterAllTools_ZeroToolSet_RegistersEveryCategory(t *testing.T) {
	reg := NewToolRegistry()
	RegisterAllTools(reg, ToolSet{})

	got := map[string]bool{}
	for _, def := range reg.List() {
		got[def.Name] = true
	}
	for _, name := range []string{
		"mtix_show",          // node tools
		"mtix_claim",         // workflow tools
		"mtix_context",       // context tools
		"mtix_dep_add",       // dependency tools
		"mtix_inbox_ack",     // inbox tools
		"mtix_session_start", // session tools
		"mtix_stats",         // analytics tools
		"mtix_docs_generate", // docs tools
		"mtix_sync_workflow", // sync workflow tool
	} {
		require.True(t, got[name], "%s is registered through RegisterAllTools", name)
	}
}
