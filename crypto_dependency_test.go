// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Crypto dependency guards pin MTIX-107.10's minimal security upgrade and the
// source-build requirements that users need to install that upgrade.
package mtix_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCryptoDependency_ApprovedVersions_ArePinned(t *testing.T) {
	content := readCryptoDocument(t, "go.mod")
	tests := []struct{ name, line string }{
		{"minimum Go", "go 1.26.0"},
		{"patched toolchain unchanged", "toolchain go1.26.6"},
		{"smallest SSH fix", "\tgolang.org/x/crypto v0.56.0 // indirect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Contains(t, "\n"+content, "\n"+tt.line+"\n")
		})
	}
}

func TestCryptoDependency_CurrentBuildDocs_RequireGo126(t *testing.T) {
	for _, file := range []string{"README.md", "CONTRIBUTING.md", "USERMANUAL.md"} {
		t.Run(file, func(t *testing.T) {
			content := readCryptoDocument(t, file)
			require.Contains(t, content, "Go 1.26+")
			require.NotContains(t, content, "Go 1.25+")
		})
	}
}

func TestCryptoDependency_ResidualAndReleaseNote_AreDocumented(t *testing.T) {
	tests := []struct{ file, text string }{
		{"SECURITY.md", "GO-2026-5932"},
		{"SECURITY.md", "deprecated"},
		{"SECURITY.md", "no fixed version"},
		{"SECURITY.md", "module-only"},
		{"SECURITY.md", "never imports"},
		{"SECURITY.md", "golang.org/x/crypto/openpgp"},
		{"CHANGELOG.md", "Building mtix from source now requires Go 1.26+"},
	}
	for _, tt := range tests {
		t.Run(tt.file+"/"+tt.text, func(t *testing.T) {
			require.Contains(t, readCryptoDocument(t, tt.file), tt.text)
		})
	}
}

func readCryptoDocument(t *testing.T, file string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(projectRoot(t), file))
	require.NoError(t, err)
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}
