// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSourceScan_NoGlobalSlogCalls_InNonTestFiles proves the package never logs
// through the process-global slog logger (MTIX-130.14). Package-level
// slog.Warn/Info/Error/Debug/Log calls are forbidden. slog.Default() is allowed
// only as the right-hand side of an assignment, the nil-logger fallback of a
// constructor or setter; it may not be called inline.
func TestSourceScan_NoGlobalSlogCalls_InNonTestFiles(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	banned := map[string]bool{"Warn": true, "Info": true, "Error": true, "Debug": true, "Log": true,
		"WarnContext": true, "InfoContext": true, "ErrorContext": true, "DebugContext": true, "LogAttrs": true}
	fset := token.NewFileSet()
	var offences []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		allowed := map[*ast.CallExpr]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok {
				for _, rhs := range as.Rhs {
					if c, ok := rhs.(*ast.CallExpr); ok {
						allowed[c] = true
					}
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "slog" &&
				(banned[sel.Sel.Name] || (sel.Sel.Name == "Default" && !allowed[call])) {
				offences = append(offences, fset.Position(call.Pos()).String()+" slog."+sel.Sel.Name)
			}
			return true
		})
	}
	require.Empty(t, offences, "use the service's or caller's logger, not the global slog logger")
}
