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

// The decision fold reads no config, stamp, clock, ack or local node state
// (ADR-008 §3.4). The behavioural half is the replica convergence test; this
// source-level half rejects any import, clock/environment call or package-level
// variable read that could smuggle such state in.
var decisionFoldSources = []string{"decision_fold.go", "decision_answers.go", "decision_supersede.go", "decision_ticket_deleted.go"}

var decisionFoldAllowedImports = map[string]bool{
	"errors": true, "maps": true, "slices": true,
	"github.com/hyper-swe/mtix/internal/model": true,
}

func TestDecisionFoldSources_ReadNoAmbientState(t *testing.T) {
	fset := token.NewFileSet()
	pkgVars := packageLevelVars(t, fset)
	for _, name := range decisionFoldSources {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err, name)
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			require.Truef(t, decisionFoldAllowedImports[path], "%s imports %q outside the fold allow-list", name, path)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := x.X.(*ast.Ident); ok && (pkg.Name == "time" || pkg.Name == "os") {
					t.Errorf("%s: %s.%s is an ambient clock or environment read", name, pkg.Name, x.Sel.Name)
				}
			case *ast.Ident:
				if pkgVars[x.Name] {
					t.Errorf("%s: %s refers to a package-level variable", name, x.Name)
				}
			}
			return true
		})
	}
}

// packageLevelVars returns every top-level var name declared in non-test files
// of this package, so a fold file reading one is caught.
func packageLevelVars(t *testing.T, fset *token.FileSet) map[string]bool {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	vars := make(map[string]bool)
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err, name)
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				for _, id := range spec.(*ast.ValueSpec).Names {
					if id.Name != "_" {
						vars[id.Name] = true
					}
				}
			}
		}
	}
	return vars
}
