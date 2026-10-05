// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Dependency help tests exercise the types advertised by Cobra through real
// commands and SQLite, so accepted values and documented values cannot drift.
package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.ElementsMatch(t, []string{"blocks", "related", "discovered_from", "duplicates"}, types)
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
	assert.ElementsMatch(t, []string{"blocks", "related", "discovered_from", "duplicates"}, types)
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
