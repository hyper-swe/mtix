// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests enforce MTIX-130.8's test-only Porcupine approval, including
// source behind build tags. Alias names and import quoting do not affect policy.
package mtix_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const porcupineModule = "github.com/anishathalye/porcupine"

func TestPorcupine_ProductionImportsAreRejected(t *testing.T) {
	require.NoError(t, inspectPorcupineTree(projectRoot(t)))
}

func inspectPorcupineTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk source %s: %w", path, walkErr)
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".mgit", ".hyperswe", ".codex-lane", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read source %s: %w", path, err)
		}
		return inspectPorcupineImports(path, source)
	})
}

func inspectPorcupineImports(path string, source []byte) error {
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
	if err != nil {
		return fmt.Errorf("parse imports %s: %w", path, err)
	}
	for _, spec := range parsed.Imports {
		imported, unquoteErr := strconv.Unquote(spec.Path.Value)
		if unquoteErr != nil {
			return fmt.Errorf("unquote import %s: %w", path, unquoteErr)
		}
		if imported == porcupineModule || strings.HasPrefix(imported, porcupineModule+"/") {
			return fmt.Errorf("production file %s imports test-only package %s", path, imported)
		}
	}
	return nil
}

type porcupineImportCase struct {
	name, path, source string
	rejected           bool
}

func porcupineImportCases() []porcupineImportCase {
	return []porcupineImportCase{
		{"ordinary", "source.go", `package source; import "github.com/anishathalye/porcupine"`, true},
		{"renamed", "source.go", `package source; import checker "github.com/anishathalye/porcupine"`, true},
		{"blank", "source.go", `package source; import _ "github.com/anishathalye/porcupine"`, true},
		{"dot", "source.go", `package source; import . "github.com/anishathalye/porcupine"`, true},
		{"raw", "source.go", "package source; import `github.com/anishathalye/porcupine`", true},
		{"escaped", "source.go", `package source; import "github.com/anishathalye/porc\x75pine"`, true},
		{"subpackage", "source.go", `package source; import "github.com/anishathalye/porcupine/internal"`, true},
		{"prefix lookalike", "source.go", `package source; import "github.com/anishathalye/porcupine-other"`, false},
		{"test allowed", "source_test.go", `package source; import "github.com/anishathalye/porcupine"`, false},
		{"unrelated", "source.go", `package source; import "fmt"`, false},
		{"comment", "source.go", `package source // import "github.com/anishathalye/porcupine"`, false},
		{"bad syntax", "source.go", `package source; import "unterminated`, true},
		{"nondefault tag", "source.go", "//go:build neverdefault\n\npackage source\nimport _ \"github.com/anishathalye/porcupine\"", true},
	}
}

func TestPorcupine_ImportPolicyChecksAliasesAndQuotes(t *testing.T) {
	for _, tc := range porcupineImportCases() {
		t.Run(tc.name, func(t *testing.T) {
			err := inspectPorcupineImports(tc.path, []byte(tc.source))
			if tc.rejected {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPorcupine_SourceTraversalFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*testing.T, string) string
	}{
		{"missing root", func(t *testing.T, root string) string { return filepath.Join(root, "missing") }},
		{"unreadable source", func(t *testing.T, root string) string {
			require.NoError(t, os.Symlink(filepath.Join(root, "absent"), filepath.Join(root, "broken.go")))
			return root
		}},
		{"parse error", func(t *testing.T, root string) string {
			require.NoError(t, os.WriteFile(filepath.Join(root, "invalid.go"), []byte("not Go"), 0o600))
			return root
		}},
		{"tagged import", func(t *testing.T, root string) string {
			require.NoError(t, os.WriteFile(filepath.Join(root, "tagged.go"), []byte(porcupineImportCases()[12].source), 0o600))
			return root
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { require.Error(t, inspectPorcupineTree(tc.prepare(t, t.TempDir()))) })
	}
}
