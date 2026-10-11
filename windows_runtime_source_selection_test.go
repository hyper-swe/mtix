// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Select real Go source files with the target build context and inspect AST
// declarations rather than duplicating workflow names or grepping comments.
package mtix_test

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func windowsNativeBuildContext() build.Context {
	context := build.Default
	context.GOOS = "windows"
	context.GOARCH = "amd64"
	context.CgoEnabled = true
	context.BuildTags = []string{"race"}
	context.UseAllFiles = false
	context.ToolTags = []string{"amd64.v1"}
	for _, tag := range build.Default.ToolTags {
		if strings.HasPrefix(tag, "goexperiment.") {
			context.ToolTags = append(context.ToolTags, tag)
		}
	}
	return context
}

func windowsAvailableTests(directory string) (map[string]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read native test directory: %w", err)
	}
	context := windowsNativeBuildContext()
	names := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		included, matchErr := context.MatchFile(directory, entry.Name())
		if matchErr != nil {
			return nil, fmt.Errorf("match Windows source %s: %w", entry.Name(), matchErr)
		}
		if !included {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, entry.Name()), nil, parser.AllErrors)
		if parseErr != nil {
			return nil, fmt.Errorf("parse Windows test source %s: %w", entry.Name(), parseErr)
		}
		for _, declaration := range file.Decls {
			function, isFunction := declaration.(*ast.FuncDecl)
			if !isFunction || !windowsRunnableTest(function, file) {
				continue
			}
			if prior := names[function.Name.Name]; prior != "" {
				return nil, fmt.Errorf("duplicate native test %s in %s and %s", function.Name.Name, prior, entry.Name())
			}
			names[function.Name.Name] = entry.Name()
		}
	}
	return names, nil
}

func windowsRunnableTest(function *ast.FuncDecl, file *ast.File) bool {
	name := function.Name.Name
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	if len(name) > 4 {
		runeAfterPrefix, _ := utf8.DecodeRuneInString(name[4:])
		if unicode.IsLower(runeAfterPrefix) {
			return false
		}
	}
	if function.Recv != nil || function.Body == nil || function.Type.TypeParams != nil || function.Type.Results != nil {
		return false
	}
	if function.Type.Params == nil || len(function.Type.Params.List) != 1 {
		return false
	}
	argument := function.Type.Params.List[0]
	if len(argument.Names) > 1 {
		return false
	}
	pointer, isPointer := argument.Type.(*ast.StarExpr)
	if !isPointer {
		return false
	}
	if identifier, isIdentifier := pointer.X.(*ast.Ident); isIdentifier {
		return identifier.Name == "T" && windowsTestingImport(file, ".")
	}
	selector, isSelector := pointer.X.(*ast.SelectorExpr)
	if !isSelector || selector.Sel.Name != "T" {
		return false
	}
	qualifier, isIdentifier := selector.X.(*ast.Ident)
	return isIdentifier && windowsTestingImport(file, qualifier.Name)
}

func windowsTestingImport(file *ast.File, qualifier string) bool {
	for _, imported := range file.Imports {
		name, err := strconv.Unquote(imported.Path.Value)
		if err != nil || name != "testing" {
			continue
		}
		alias := "testing"
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		if alias == qualifier {
			return true
		}
	}
	return false
}
