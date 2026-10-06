// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// OpenPGP import guards keep the unfixed, module-only GO-2026-5932 advisory
// unreachable, including source omitted by the current platform or build tags.
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

func TestOpenPGPImportGuard_Repository_HasNoImports(t *testing.T) {
	require.NoError(t, openPGPImportError(projectRoot(t)))
}

func TestOpenPGPImportGuard_ForbiddenImport_IsRejected(t *testing.T) {
	tests := openPGPForbiddenImports()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeOpenPGPFixture(t, root, "import.go", "package fixture\n"+tt.source)
			err := openPGPImportError(root)
			require.ErrorContains(t, err, "forbidden OpenPGP import")
			require.ErrorContains(t, err, "import.go")
		})
	}
}

type openPGPImportCase struct{ name, source string }

func openPGPForbiddenImports() []openPGPImportCase {
	return []openPGPImportCase{
		{"ordinary", `import "golang.org/x/crypto/openpgp"`},
		{"alias", `import pgp "golang.org/x/crypto/openpgp"`},
		{"blank", `import _ "golang.org/x/crypto/openpgp"`},
		{"dot", `import . "golang.org/x/crypto/openpgp"`},
		{"subpackage", `import "golang.org/x/crypto/openpgp/packet"`},
		{"nested subpackage", `import "golang.org/x/crypto/openpgp/packet/sub"`},
		{"raw", "import `golang.org/x/crypto/openpgp`"},
		{"raw aliased subpackage", "import pgp `golang.org/x/crypto/openpgp/armor`"},
		{"hex escaped", `import "golang.org/x/crypto/\x6fpenpgp"`},
		{"unicode escaped", `import "golang.org/x/crypto/\u006fpenpgp/armor"`},
		{"octal escaped", `import "golang.org/x/crypto/\157penpgp"`},
		{"grouped", "import (\n \"fmt\"\n _ \"golang.org/x/crypto/openpgp\"\n)"},
		{"comment before path", `import _ /* retained import */ "golang.org/x/crypto/openpgp"`},
	}
}

func TestOpenPGPImportGuard_AllowedSource_IsAccepted(t *testing.T) {
	tests := []openPGPImportCase{
		{"no imports", "const value = 1"},
		{"comment decoy", `// import "golang.org/x/crypto/openpgp"`},
		{"string decoy", `const value = "golang.org/x/crypto/openpgp"`},
		{"other crypto", `import _ "golang.org/x/crypto/ssh"`},
		{"prefix lookalike", `import _ "golang.org/x/crypto/openpgpfoo"`},
		{"suffix lookalike", `import _ "example.org/golang.org/x/crypto/openpgp"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeOpenPGPFixture(t, root, "import.go", "package fixture\n"+tt.source)
			require.NoError(t, openPGPImportError(root))
		})
	}
}

func TestOpenPGPImportGuard_SourceLocations_AreScanned(t *testing.T) {
	tests := []struct{ name, path, prefix string }{
		{"nondefault build tag", "tagged.go", "//go:build mtix_openpgp_probe\n\n"},
		{"other OS", "import_windows.go", ""},
		{"test source", "import_test.go", ""},
		{"hidden repository source", ".custom/import.go", ""},
		{"hidden nested source", "internal/.custom/import.go", ""},
		{"nested metadata name", "internal/.hyperswe/import.go", ""},
		{"web source", "web/import.go", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			source := tt.prefix + "package fixture\nimport _ \"golang.org/x/crypto/openpgp\"\n"
			writeOpenPGPFixture(t, root, tt.path, source)
			require.ErrorContains(t, openPGPImportError(root), "forbidden OpenPGP import")
		})
	}
}

func TestOpenPGPImportGuard_ExternalAndBookkeeping_AreExcluded(t *testing.T) {
	paths := []string{
		".git/import.go", ".mgit/import.go", ".hyperswe/import.go", ".mtix/import.go",
		"vendor/import.go", "web/node_modules/import.go", "internal/vendor/import.go",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			writeOpenPGPFixture(t, root, path, "package fixture\nimport _ \"golang.org/x/crypto/openpgp\"\n")
			require.NoError(t, openPGPImportError(root))
		})
	}
}

func TestOpenPGPImportGuard_InvalidInput_FailsClosed(t *testing.T) {
	tests := []struct{ name, message string }{
		{"missing root", "walk Go source"},
		{"malformed import", "parse Go imports"},
		{"unreadable source target", "parse Go imports"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			switch tt.name {
			case "missing root":
				root = filepath.Join(root, "missing")
			case "malformed import":
				writeOpenPGPFixture(t, root, "import.go", "package fixture\nimport \"unterminated\n")
			case "unreadable source target":
				require.NoError(t, os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "import.go")))
			}
			require.ErrorContains(t, openPGPImportError(root), tt.message)
		})
	}
}

func writeOpenPGPFixture(t *testing.T, root, path, source string) {
	t.Helper()
	file := filepath.Join(root, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, os.WriteFile(file, []byte(source), 0o600))
}

// openPGPImportError examines physical source, without go list's build filtering
// or a Git checkout. Only checkout metadata and vendored external code are
// omitted; arbitrary hidden repository directories are deliberately scanned.
func openPGPImportError(root string) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk Go source %s: %w", path, walkErr)
		}
		if entry.IsDir() {
			if openPGPExcludedDirectory(root, path, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		return openPGPSourceImportError(path)
	})
	if err != nil {
		return fmt.Errorf("scan repository for OpenPGP imports: %w", err)
	}
	return nil
}

func openPGPExcludedDirectory(root, path, name string) bool {
	if name == "vendor" || name == "node_modules" {
		return path != root
	}
	for _, metadata := range []string{".git", ".mgit", ".hyperswe", ".mtix"} {
		if path == filepath.Join(root, metadata) {
			return true
		}
	}
	return false
}

func openPGPSourceImportError(path string) error {
	source, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return fmt.Errorf("parse Go imports %s: %w", path, err)
	}
	for _, spec := range source.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return fmt.Errorf("decode Go import %s: %w", path, err)
		}
		if imported == "golang.org/x/crypto/openpgp" || strings.HasPrefix(imported, "golang.org/x/crypto/openpgp/") {
			return fmt.Errorf("forbidden OpenPGP import %q in %s (GO-2026-5932)", imported, path)
		}
	}
	return nil
}
