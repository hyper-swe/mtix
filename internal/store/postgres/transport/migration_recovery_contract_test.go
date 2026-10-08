// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// This STRUCTURAL contract guards the fixture, not pgx error semantics.
// Runtime lock, rollback and recovery assertions provide the behavioral proof.
// It catches direct calls, imported aliases and simple local aliases. Dynamic function
// indirection is outside this guard; do not replace the server assertions.
func TestMigrationRecoveryFixture_ObservedCancellationContract(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "integration_test.go", nil, 0)
	require.NoError(t, err)
	var fixture *ast.FuncDecl
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "TestMigrate_PartialMigrationRecovery" {
			fixture = fn
		}
	}
	require.NotNil(t, fixture)
	calls := migrationRecoveryCalls(fixture)
	auxiliary := migrationRecoveryHelperCalls(t)
	allCalls := append(append([]migrationRecoveryCall(nil), calls...), auxiliary...)
	tests := []struct {
		name  string
		valid bool
	}{
		{"no arbitrary sleep", !migrationRecoveryHas(allCalls, "Sleep")},
		{"no timed migration cancellation", !migrationRecoveryHas(calls, "WithTimeout") && !migrationRecoveryHas(calls, "WithDeadline")},
		{"no skipped chaos coverage", !migrationRecoveryHas(allCalls, "Skip") && !migrationRecoveryHas(allCalls, "Skipf") && !migrationRecoveryHas(allCalls, "SkipNow")},
		{"observe schema lock before cancellation", migrationRecoveryObservedBeforeCancel(calls)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { require.True(t, test.valid, "STRUCTURAL fixture contract: %s", test.name) })
	}
}

type migrationRecoveryCall struct {
	name     string
	position token.Pos
}

func migrationRecoveryCalls(fn *ast.FuncDecl) []migrationRecoveryCall {
	var calls []migrationRecoveryCall
	aliases := migrationRecoveryAliases(fn)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := migrationRecoveryCallName(call.Fun, aliases)
		calls = append(calls, migrationRecoveryCall{name, call.Pos()})
		return true
	})
	return calls
}

func migrationRecoveryHas(calls []migrationRecoveryCall, name string) bool {
	for _, call := range calls {
		if call.name == name {
			return true
		}
	}
	return false
}

func migrationRecoveryObservedBeforeCancel(calls []migrationRecoveryCall) bool {
	var observed, canceled token.Pos
	for _, call := range calls {
		if call.name == "waitForSchemaLock" && observed == 0 {
			observed = call.position
		}
		if call.name == "cancel" && canceled == 0 {
			canceled = call.position
		}
	}
	return observed != 0 && canceled != 0 && observed < canceled
}

// Check direct calls in the dedicated helpers too. General transitive dynamic
// call graphs and deliberately hidden function indirection are known limits.
func migrationRecoveryHelperCalls(t *testing.T) []migrationRecoveryCall {
	t.Helper()
	const name = "migration_recovery_fixture_test.go"
	_, err := os.Stat(name)
	if os.IsNotExist(err) {
		return nil
	} // old compiled fixture has no helper file
	require.NoError(t, err)
	f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	require.NoError(t, err)
	var calls []migrationRecoveryCall
	for _, declaration := range f.Decls {
		if fn, ok := declaration.(*ast.FuncDecl); ok {
			calls = append(calls, migrationRecoveryCalls(fn)...)
		}
	}
	return calls
}

func TestMigrationRecoveryContract_DirectLocalAliases(t *testing.T) {
	tests := []struct{ name, body string }{
		{"direct", "time.Sleep(0)"},
		{"assignment", "pause := time.Sleep; pause(0)"},
		{"declaration", "var pause = time.Sleep; pause(0)"},
		{"multiple RHS", "unused, pause := true, time.Sleep; _ = unused; pause(0)"},
		{"alias chain", "pause := time.Sleep; wait := pause; wait(0)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Parser inputs exercise structural analysis, not runtime sleep behavior.
			f, err := parser.ParseFile(token.NewFileSet(), "alias.go", "package fixture; func probe(){"+test.body+"}", 0)
			require.NoError(t, err)
			fn := f.Decls[0].(*ast.FuncDecl)
			calls := migrationRecoveryCalls(fn)
			require.True(t, migrationRecoveryHas(calls, "Sleep"), "simple alias must retain Sleep target")
		})
	}
}

func migrationRecoveryCallName(expr ast.Expr, aliases map[string]string) string {
	switch value := expr.(type) {
	case *ast.SelectorExpr:
		return value.Sel.Name
	case *ast.Ident:
		if name, ok := aliases[value.Name]; ok {
			return name
		}
		return value.Name
	}
	return ""
}

func migrationRecoveryAliases(fn *ast.FuncDecl) map[string]string {
	aliases := make(map[string]string)
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch statement := node.(type) {
		case *ast.AssignStmt:
			if len(statement.Lhs) == len(statement.Rhs) {
				for index, lhs := range statement.Lhs {
					if name, ok := lhs.(*ast.Ident); ok {
						aliases[name.Name] = migrationRecoveryCallName(statement.Rhs[index], aliases)
					}
				}
			}
		case *ast.ValueSpec:
			if len(statement.Names) == len(statement.Values) {
				for index, name := range statement.Names {
					aliases[name.Name] = migrationRecoveryCallName(statement.Values[index], aliases)
				}
			}
		}
		return true
	})
	return aliases
}
