// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests enforce SQL Rule 1/1a and exact production query pins.
func TestSQLLiterals_ExplainDeclarations(t *testing.T) {
	tests := []struct{ file, name string }{
		{"late_events_query_shape_test.go", "explainListEventIDsSince"},
		{"late_events_query_shape_test.go", "explainFetchEventsByID"},
		{"push_presence_query_shape_test.go", "explainPresence"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), tt.file, nil, 0)
			require.NoError(t, err)
			found := false
			ast.Inspect(f, func(n ast.Node) bool {
				spec, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for i, name := range spec.Names {
					if name.Name != tt.name {
						continue
					}
					found = true
					_, literal := spec.Values[i].(*ast.BasicLit)
					require.True(t, literal, "executed EXPLAIN must be a literal")
				}
				return true
			})
			require.True(t, found)
		})
	}
}

func TestSQLLiterals_ExplainMatchesProduction(t *testing.T) {
	require.Equal(t, "EXPLAIN "+listEventIDsSinceSQL, explainListEventIDsSince)
	require.Equal(t, "EXPLAIN "+fetchEventsByIDSQL, explainFetchEventsByID)
	require.Equal(t, "EXPLAIN "+presenceSQL, explainPresence)
}

func TestSQLLiterals_StatementTimeoutBoundSessionSetting(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "pool.go", nil, 0)
	require.NoError(t, err)
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Exec" {
			return true
		}
		require.Len(t, call.Args, 3, "timeout value must be bound")
		lit, ok := call.Args[1].(*ast.BasicLit)
		require.True(t, ok, "timeout statement must be literal")
		if ok {
			text, err := strconv.Unquote(lit.Value)
			require.NoError(t, err)
			require.Equal(t, "SELECT set_config('statement_timeout', $1, false)", text)
			found = true
		}
		return true
	})
	require.True(t, found)
}

func TestSQLLiterals_MigrateDocumentsConstantDDL(t *testing.T) {
	body, err := os.ReadFile("migrate.go")
	require.NoError(t, err)
	require.Contains(t, string(body), "SQL Rule 1a")
	require.Contains(t, string(body), "embedded migration")
}
