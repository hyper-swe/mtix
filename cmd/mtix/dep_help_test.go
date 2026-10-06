// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Dependency help tests exercise the types advertised by Cobra through real
// commands and SQLite, so accepted values and documented values cannot drift.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/model"
)

func helpDependencyTypes(t *testing.T, cmd *cobra.Command) []string {
	t.Helper()
	help, err := executeCmd(cmd, "--help")
	require.NoError(t, err)
	const prefix = "Dependency type ("
	_, list, found := strings.Cut(help, prefix)
	require.True(t, found, "help must advertise dependency types")
	list, _, found = strings.Cut(list, ")")
	require.True(t, found, "dependency type list must close")
	return strings.Split(list, ", ")
}

func TestDepAddCmd_HelpListedTypes_AcceptedAndStored(t *testing.T) {
	types := helpDependencyTypes(t, newDepAddCmd())
	assert.ElementsMatch(t, canonicalDependencyTypeNames(), types)
	for _, depType := range types {
		t.Run(depType, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Dependency source", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("Dependency target", "", "", 3, "", "", "", "", ""))
			_, err := executeCmd(newDepAddCmd(), "TEST-1", "TEST-2", "--type", depType)
			require.NoError(t, err, "every help-listed type must work")
			// Read back the real inserted relationship to reject no-op command wiring.
			var storedType string
			err = app.store.WriteDB().QueryRow(`SELECT dep_type FROM dependencies WHERE from_id = ? AND to_id = ?`, "TEST-1", "TEST-2").Scan(&storedType)
			require.NoError(t, err)
			assert.Equal(t, depType, storedType)
		})
	}
}

func TestDepRemoveCmd_HelpListedTypes_AcceptedAndRemoved(t *testing.T) {
	types := helpDependencyTypes(t, newDepRemoveCmd())
	assert.ElementsMatch(t, canonicalDependencyTypeNames(), types)
	for _, depType := range types {
		t.Run(depType, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Dependency source", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("Dependency target", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runDepAdd("TEST-1", "TEST-2", depType))
			_, err := executeCmd(newDepRemoveCmd(), "TEST-1", "TEST-2", "--type", depType)
			require.NoError(t, err)
			// Verify the real relationship is gone, rather than trusting command success.
			var count int
			err = app.store.WriteDB().QueryRow(`SELECT count(*) FROM dependencies WHERE from_id = ? AND to_id = ?`, "TEST-1", "TEST-2").Scan(&count)
			require.NoError(t, err)
			assert.Zero(t, count)
		})
	}
}

func TestDepAddCmd_InvalidTypes_ReturnInvalidInput(t *testing.T) {
	for _, depType := range []string{"", "relates_to", "unknown", "RELATED"} {
		t.Run(depType, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Dependency source", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("Dependency target", "", "", 3, "", "", "", "", ""))
			_, err := executeCmd(newDepAddCmd(), "TEST-1", "TEST-2", "--type", depType)
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

// canonicalDependencyTypeNames derives expectations from the validator's one
// model-defined source; future FR-4.2 types are not duplicated in these tests.
func canonicalDependencyTypeNames() []string {
	names := []string{}
	for _, kind := range model.AllDepTypes() {
		names = append(names, string(kind))
	}
	return names
}

// TestDepAddCmd_TypeHelp_ListsExactlyModelTypes is the MTIX-96 acceptance test.
// Historical red evidence uses a compiling pre-MTIX-107.92 compatibility harness;
// current producers already use AllDepTypes and must remain model-derived.
func TestDepAddCmd_TypeHelp_ListsExactlyModelTypes(t *testing.T) {
	for _, cmd := range []*cobra.Command{newDepAddCmd(), newDepRemoveCmd()} {
		require.Equal(t, canonicalDependencyTypeNames(), helpDependencyTypes(t, cmd))
	}
	for _, kind := range model.AllDepTypes() {
		require.True(t, kind.IsValid(), "%s", kind)
	}
}

func TestDepHelp_MCPAndReferenceMatchModelTypes(t *testing.T) {
	registry := mcp.NewToolRegistry()
	mcp.RegisterDepTools(registry, nil)
	seen := 0
	for _, tool := range registry.List() {
		if tool.Name != "mtix_dep_add" && tool.Name != "mtix_dep_remove" {
			continue
		}
		seen++
		prop := tool.InputSchema.Properties["dep_type"]
		require.Equal(t, canonicalDependencyTypeNames(), prop.Enum, tool.Name)
		require.Equal(t, "Dependency type: "+strings.Join(canonicalDependencyTypeNames(), ", "), prop.Description, tool.Name)
	}
	require.Equal(t, 2, seen)
	shipped, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	generated := generatedHelpReference(t)
	for _, cmd := range []*cobra.Command{newDepAddCmd(), newDepRemoveCmd()} {
		require.Equal(t, cliReferenceSection(t, generated, cmd.Name(), cmd.Use), cliReferenceSection(t, string(shipped), cmd.Name(), cmd.Use))
	}
}

func TestDepAddCmd_RelatedPersistsActualDependency(t *testing.T) {
	initTestApp(t)
	for _, title := range []string{"source", "target"} {
		require.NoError(t, runCreate(title, "", "", 3, "", "", "", "", ""))
	}
	_, err := executeCmd(newDepAddCmd(), "TEST-1", "TEST-2", "--type", string(model.DepTypeRelated))
	require.NoError(t, err)
	var stored string
	require.NoError(t, app.store.WriteDB().QueryRow(`SELECT dep_type FROM dependencies WHERE from_id = ? AND to_id = ?`, "TEST-1", "TEST-2").Scan(&stored))
	require.Equal(t, string(model.DepTypeRelated), stored)
}
