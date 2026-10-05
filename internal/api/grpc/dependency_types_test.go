// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

// gRPC DTO boundary tests verify dependency parity against real SQLite, plus the proto contract.
import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestGRPC_Dependencies_AllModelTypesPersistAndRemove(t *testing.T) {
	for _, depType := range model.AllDepTypes() {
		t.Run(string(depType), func(t *testing.T) {
			s := testGRPCServer(t)
			from := createTestNode(t, s, "Dependency source", "TEST")
			to := createTestNode(t, s, "Dependency target", "TEST")
			ctx := context.Background()
			require.NoError(t, s.HandleAddDependency(ctx, &model.Dependency{FromID: from.ID, ToID: to.ID, DepType: depType}))
			var count int
			// Inspect exact persisted type rather than the blocks-only query.
			query := "SELECT count(*) FROM dependencies WHERE from_id = ? AND to_id = ? AND dep_type = ?"
			require.NoError(t, s.store.ReadDB().QueryRowContext(ctx, query, from.ID, to.ID, depType).Scan(&count))
			assert.Equal(t, 1, count)
			require.NoError(t, s.HandleRemoveDependency(ctx, from.ID, to.ID, depType))
			require.NoError(t, s.store.ReadDB().QueryRowContext(ctx, query, from.ID, to.ID, depType).Scan(&count))
			assert.Zero(t, count)
		})
	}
}

func TestGRPC_DependencyProto_AllModelTypesHaveWireValues(t *testing.T) {
	content, err := os.ReadFile("../../../proto/mtix/v1/types.proto")
	require.NoError(t, err)
	_, enum, found := strings.Cut(string(content), "enum DepType {")
	require.True(t, found)
	enum, _, found = strings.Cut(enum, "}")
	require.True(t, found)
	enum = strings.Join(strings.Fields(enum), " ")
	expected := []string{"DEP_TYPE_UNSPECIFIED = 0;", "DEP_TYPE_BLOCKS = 1;", "DEP_TYPE_RELATED = 2;", "DEP_TYPE_DISCOVERED_FROM = 3;", "DEP_TYPE_DUPLICATES = 4;"}
	for _, value := range expected {
		assert.Contains(t, enum, value)
	}
	assert.Equal(t, len(model.AllDepTypes())+1, strings.Count(enum, "="))
	for _, depType := range model.AllDepTypes() {
		assert.Contains(t, enum, "DEP_TYPE_"+strings.ToUpper(string(depType))+" =")
	}
}
