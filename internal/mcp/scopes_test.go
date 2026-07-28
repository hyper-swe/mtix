// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-2.1.3: tool scopes + a read-only client mode.

func TestScopeForTool_Classifies(t *testing.T) {
	assert.Equal(t, ScopeRead, scopeForTool("mtix_show"))
	assert.Equal(t, ScopeRead, scopeForTool("mtix_list"))
	assert.Equal(t, ScopeRead, scopeForTool("mtix_sync_workflow"))
	assert.Equal(t, ScopeWrite, scopeForTool("mtix_create"))
	assert.Equal(t, ScopeWrite, scopeForTool("mtix_claim"))
	assert.Equal(t, ScopeAdmin, scopeForTool("mtix_delete"))
	assert.Equal(t, ScopeAdmin, scopeForTool("mtix_rerun"))
	assert.Equal(t, ScopeWrite, scopeForTool("mtix_something_new"),
		"an unclassified tool defaults to write (fail-safe: a read-only client cannot call it)")
}

func okHandler(context.Context, json.RawMessage) (*ToolsCallResult, error) {
	return SuccessResult("ok"), nil
}

// buildScopedRegistry registers one tool of each scope.
func buildScopedRegistry() *ToolRegistry {
	reg := NewToolRegistry()
	reg.Register(ToolDef{Name: "mtix_show"}, okHandler)   // read
	reg.Register(ToolDef{Name: "mtix_create"}, okHandler) // write
	reg.Register(ToolDef{Name: "mtix_delete"}, okHandler) // admin
	return reg
}

func TestRegistry_AutoAssignsScope(t *testing.T) {
	reg := buildScopedRegistry()
	byName := map[string]ToolScope{}
	for _, d := range reg.List() {
		byName[d.Name] = d.Scope
	}
	assert.Equal(t, ScopeRead, byName["mtix_show"])
	assert.Equal(t, ScopeWrite, byName["mtix_create"])
	assert.Equal(t, ScopeAdmin, byName["mtix_delete"])
}

func TestRegistry_ReadOnly_FiltersListToReadTools(t *testing.T) {
	reg := buildScopedRegistry()
	reg.SetReadOnly(true)

	names := []string{}
	for _, d := range reg.List() {
		names = append(names, d.Name)
	}
	assert.Equal(t, []string{"mtix_show"}, names,
		"a read-only client only discovers read-scoped tools")
}

func TestRegistry_ReadOnly_RejectsMutationCalls(t *testing.T) {
	reg := buildScopedRegistry()
	reg.SetReadOnly(true)
	ctx := context.Background()

	// Read tool still works.
	_, err := reg.Call(ctx, "mtix_show", nil)
	require.NoError(t, err)

	// Write and admin tools are refused with ErrReadOnly (defense in depth —
	// even though List hid them).
	_, err = reg.Call(ctx, "mtix_create", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrReadOnly))

	_, err = reg.Call(ctx, "mtix_delete", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrReadOnly))
}

func TestRegistry_NotReadOnly_AllowsEverything(t *testing.T) {
	reg := buildScopedRegistry()
	ctx := context.Background()
	assert.Len(t, reg.List(), 3)
	for _, name := range []string{"mtix_show", "mtix_create", "mtix_delete"} {
		_, err := reg.Call(ctx, name, nil)
		require.NoErrorf(t, err, "%s must be callable when not read-only", name)
	}
}

// TestReadOnly_RealNodeTools exercises the gate against actually-registered
// tools: the node group has reads (mtix_show/list) and mutations
// (mtix_create/update/delete). A read-only client lists the reads and is
// refused the mutations.
func TestReadOnly_RealNodeTools(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})
	reg.SetReadOnly(true)
	ctx := context.Background()

	listed := map[string]bool{}
	for _, d := range reg.List() {
		listed[d.Name] = true
		assert.Equal(t, ScopeRead, d.Scope, "read-only list must contain only read tools; got %s", d.Name)
	}
	assert.True(t, listed["mtix_show"], "reads remain available")
	assert.False(t, listed["mtix_create"], "mutations are hidden")
	assert.False(t, listed["mtix_delete"], "destructive tools are hidden")

	_, err := reg.Call(ctx, "mtix_create", json.RawMessage(`{}`))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrReadOnly))
}
