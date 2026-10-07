// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

// These tests exercise advertised dependency types through the real registry and SQLite.
import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func dependencyRegistry(t *testing.T) (*ToolRegistry, *sqlite.Store, string, string) {
	t.Helper()
	st, err := sqlite.New(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	from := &model.Node{ID: "TEST-1", Project: "TEST", Title: "Dependency source", Status: model.StatusOpen}
	to := &model.Node{ID: "TEST-2", Project: "TEST", Title: "Dependency target", Status: model.StatusOpen}
	require.NoError(t, st.CreateNode(context.Background(), from))
	require.NoError(t, st.CreateNode(context.Background(), to))
	reg := NewToolRegistry()
	RegisterDepTools(reg, testDependencyService(st))
	return reg, st, from.ID, to.ID
}

func TestDepTools_SchemasMatchAllDepTypes(t *testing.T) {
	reg, _, _, _ := dependencyRegistry(t)
	expected := make([]string, 0, len(model.AllDepTypes()))
	for _, depType := range model.AllDepTypes() {
		expected = append(expected, string(depType))
	}
	for _, tool := range reg.List() {
		if tool.Name == "mtix_dep_show" {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			prop := tool.InputSchema.Properties["dep_type"]
			assert.Equal(t, expected, prop.Enum)
			assert.Equal(t, "Dependency type: "+strings.Join(expected, ", "), prop.Description)
		})
	}
}

func TestDepTools_EveryAdvertisedTypePersistsAndRemoves(t *testing.T) {
	reg, _, _, _ := dependencyRegistry(t)
	for _, tool := range reg.List() {
		if tool.Name == "mtix_dep_show" {
			continue
		}
		for _, depType := range tool.InputSchema.Properties["dep_type"].Enum {
			t.Run(tool.Name+"/"+depType, func(t *testing.T) {
				registry, st, from, to := dependencyRegistry(t)
				args, err := json.Marshal(map[string]string{"from_id": from, "to_id": to, "dep_type": depType})
				require.NoError(t, err)
				if tool.Name == "mtix_dep_remove" {
					require.NoError(t, st.AddDependency(context.Background(), &model.Dependency{FromID: from, ToID: to, DepType: model.DepType(depType)}))
				}
				result, err := registry.Call(context.Background(), tool.Name, args)
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.False(t, result.IsError)
				var count int
				// Read the exact edge, including informational types that GetBlockers excludes.
				require.NoError(t, st.ReadDB().QueryRowContext(context.Background(), "SELECT count(*) FROM dependencies WHERE from_id = ? AND to_id = ? AND dep_type = ?", from, to, depType).Scan(&count))
				expected := 1
				if tool.Name == "mtix_dep_remove" {
					expected = 0
				}
				assert.Equal(t, expected, count)
			})
		}
	}
}

func TestDepAddTool_NeedsInputRejectedWithoutPersistence(t *testing.T) {
	reg, st, from, to := dependencyRegistry(t)
	args, err := json.Marshal(map[string]string{"from_id": from, "to_id": to, "dep_type": "needs_input"})
	require.NoError(t, err)
	_, err = reg.Call(context.Background(), "mtix_dep_add", args)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	var count int
	// Invalid input must leave the edge table unchanged.
	require.NoError(t, st.ReadDB().QueryRowContext(context.Background(), "SELECT count(*) FROM dependencies WHERE from_id = ? AND to_id = ?", from, to).Scan(&count))
	assert.Zero(t, count)
}
