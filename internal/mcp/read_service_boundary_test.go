// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"github.com/stretchr/testify/require"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Storage imports also expose simple local/package aliases; parse every
// production file, including inactive build tags and all import quote forms.
// Deliberate reflection/asserted aliases or dynamically supplied raw backend
// closures remain the seq3345 review limit, not a whole-program analysis claim.
func TestReadServiceBoundary_ProductionHasNoStorageDependency(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Empty(t, readStorageImports(t, string(raw)), "%s must depend on services", path)
	}
}

func readStorageImports(t *testing.T, raw string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "source.go", raw, parser.ImportsOnly)
	require.NoError(t, err)
	var imports []string
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		require.NoError(t, err)
		if path == "database/sql" || path == "github.com/hyper-swe/mtix/internal/store" || strings.HasPrefix(path, "github.com/hyper-swe/mtix/internal/store/") {
			imports = append(imports, path)
		}
	}
	return imports
}

func TestReadServiceBoundary_OrdinaryAliasesAndImportQuoting(t *testing.T) {
	for _, source := range []string{
		`import s "github.com/hyper-swe/mtix/internal/store"; type backend = s.Store`,
		"import s `github.com/hyper-swe/mtix/internal/store/sqlite`; var backend *s.Store",
		`import . "github.com/hyper-swe/mtix/internal/store"; type backend = Store`,
		`import s "github.com/hyper-swe/mtix/internal/sto\x72e"; type backend = s.Store`,
		`import db "database/sql"; var backend *db.DB`,
	} {
		require.Len(t, readStorageImports(t, "package sample\n"+source), 1)
	}
	require.Empty(t, readStorageImports(t, `package sample; import "github.com/hyper-swe/mtix/internal/service"`))
}
