// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
)

// registeredNames returns the sorted tool names on reg.
func registeredNames(reg *mcp.ToolRegistry) []string {
	var names []string
	for _, def := range reg.List() {
		names = append(names, def.Name)
	}
	sort.Strings(names)
	return names
}

// TestRegisterMCPTools_NamesMatchSharedRegistration: the tools the mtix mcp
// command registers are exactly the tools the documentation guard sees,
// because both call mcp.RegisterAllTools (MTIX-107.42).
func TestRegisterMCPTools_NamesMatchSharedRegistration(t *testing.T) {
	initTestAppWithAuthorConfig(t, "")

	served := mcp.NewToolRegistry()
	registerMCPTools(served)
	guarded := mcp.NewToolRegistry()
	mcp.RegisterAllTools(guarded, mcp.ToolSet{})

	require.NotEmpty(t, registeredNames(served))
	require.Equal(t, registeredNames(guarded), registeredNames(served))
}
