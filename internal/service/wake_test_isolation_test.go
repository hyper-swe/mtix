// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Wake config tests must isolate process state or inject an explicit state view.
// A validation mutant must never reach the developer's actual operator files.
func TestWakeConfigTests_OperatorStateIsIsolated(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		source, err := os.ReadFile(entry.Name())
		require.NoError(t, err)
		require.Empty(t, unisolatedWakeConfigTests(t, entry.Name(), source), "wake tests need testutil.OperatorState or WithWakeSettings: %s", entry.Name())
	}
}

func TestWakeConfigIsolationGuard_DetectsUnsafeConstructor(t *testing.T) {
	for _, tt := range []struct {
		setup  string
		unsafe bool
	}{
		{`cs, _ := service.NewConfigService(""); _, _ = cs.Set("wake.live", "off")`, true},
		{`testutil.OperatorState(t); cs, _ := service.NewConfigService(""); _, _ = cs.Set("wake.live", "off")`, false},
		{`cs, _ := service.NewConfigService(""); cs = cs.WithWakeSettings(options)`, false},
	} {
		source := []byte("package fixture; func TestWakeFixture(t *testing.T){" + tt.setup + "}")
		got := unisolatedWakeConfigTests(t, "fixture_test.go", source)
		require.Equal(t, tt.unsafe, len(got) > 0)
	}
}

func unisolatedWakeConfigTests(t *testing.T, name string, source []byte) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, source, 0)
	require.NoError(t, err)
	var unsafe []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if !strings.Contains(fn.Name.Name, "Wake") && !strings.Contains(name, "wake_") {
			continue
		}
		constructor, isolated := false, false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "NewConfigService":
				constructor = true
			case "OperatorState", "WithWakeSettings":
				isolated = true
			}
			return true
		})
		if constructor && !isolated {
			unsafe = append(unsafe, fn.Name.Name)
		}
	}
	return unsafe
}
