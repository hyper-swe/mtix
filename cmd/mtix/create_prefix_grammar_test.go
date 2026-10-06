// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Prevent accidental direct copies of the local prefix regexp (MTIX-107.3).
// This guard intentionally excludes deliberate evasion and the broader sync grammar.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const localCreatePrefixGrammar = `^[A-Z][A-Z0-9-]{0,19}$`

func duplicatePrefixGrammar(source []byte) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "prefix.go", source, 0)
	if err != nil {
		return false, err
	}
	aliases := map[string]bool{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return false, err
		}
		if path != "regexp" {
			continue
		}
		alias := "regexp"
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		aliases[alias] = true
	}
	constants := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, value := range spec.Values {
			if i < len(spec.Names) {
				constants[spec.Names[i].Name] = prefixStringLiteral(value)
			}
		}
		return true
	})
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 || !prefixRegexpCompile(call.Fun, aliases) {
			return true
		}
		grammar := prefixStringLiteral(call.Args[0])
		if ident, ok := call.Args[0].(*ast.Ident); ok {
			grammar = constants[ident.Name]
		}
		found = found || grammar == localCreatePrefixGrammar
		return true
	})
	return found, nil
}

func prefixStringLiteral(expr ast.Expr) string {
	value, ok := expr.(*ast.BasicLit)
	if !ok || value.Kind != token.STRING {
		return ""
	}
	text, err := strconv.Unquote(value.Value)
	if err != nil {
		return ""
	}
	return text
}

func prefixRegexpCompile(expr ast.Expr, aliases map[string]bool) bool {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		pkg, ok := sel.X.(*ast.Ident)
		return ok && aliases[pkg.Name] && (sel.Sel.Name == "Compile" || sel.Sel.Name == "MustCompile")
	}
	name, ok := expr.(*ast.Ident)
	return ok && aliases["."] && (name.Name == "Compile" || name.Name == "MustCompile")
}

func TestCreatePrefixGrammar_OneLocalDefinition(t *testing.T) {
	for _, dir := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			found, err := duplicatePrefixGrammar(source)
			if err != nil {
				return err
			}
			if filepath.ToSlash(path) == "../../internal/model/id.go" {
				assert.True(t, found, "model/id.go must own local create grammar")
			} else {
				assert.False(t, found, "%s duplicates local create grammar; use model.ValidatePrefix", path)
			}
			return nil
		})
		require.NoError(t, err)
	}
}

func TestCreatePrefixGrammar_DirectCopiesDetected(t *testing.T) {
	tests := []struct {
		name, source string
		duplicate    bool
	}{
		{"quoted import", "package p; import \"regexp\"; var p = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,19}$`)", true},
		{"raw import alias", "package p; import rx `regexp`; var p = rx.MustCompile(\"^[A-Z][A-Z0-9-]{0,19}$\")", true},
		{"ordinary constant", "package p; import rx \"regexp\"; const grammar = `^[A-Z][A-Z0-9-]{0,19}$`; var p, _ = rx.Compile(grammar)", true},
		{"dot import", "package p; import . \"regexp\"; var p = MustCompile(`^[A-Z][A-Z0-9-]{0,19}$`)", true},
		{"sync intentionally broader", "package p; import \"regexp\"; var p = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{0,19}$`)", false},
		{"shared validator", "package p; import \"github.com/hyper-swe/mtix/internal/model\"; func f(p string) error { return model.ValidatePrefix(p) }", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, err := duplicatePrefixGrammar([]byte(tt.source))
			require.NoError(t, err)
			assert.Equal(t, tt.duplicate, found)
		})
	}
}
