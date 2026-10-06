// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mtix_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Exercise the committed Makefile in a Git-free fixture. The unrelated lint
// backend is isolated; the real formatting executable and recipe run unchanged.
func TestLintGo_UnformattedGoFiles_RejectsAndAcceptsFormatted(t *testing.T) {
	cases := []struct{ name, filename, source string }{
		{"ordinary space path", "sample space.go", "package fixture\nfunc Value( )int{return 1}\n"},
		{"test source", "sample_test.go", "package fixture\nimport \"testing\"\nfunc TestValue( t *testing.T ){if 1!=1{t.Fatal(\"bad\")}}\n"},
		{"build tagged source", "tagged.go", "//go:build fmtfixture\n\npackage fixture\nfunc Tagged( )int{return 2}\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := formattingGateFixture(t, tt.filename, tt.source)
			compile := exec.Command("go", "test", "-tags=fmtfixture", "-run=^$", "./...")
			compile.Dir = dir
			output, err := compile.CombinedOutput()
			require.NoError(t, err, "valid fixture must compile with its tag: %s", output)
			output, err = runFormattingLint(t, dir)
			require.Error(t, err, "actual lint-go accepted unformatted source: %s", output)
			require.Contains(t, string(output), tt.filename)
			format := exec.Command("gofmt", "-w", filepath.Join(dir, tt.filename))
			require.NoError(t, format.Run())
			output, err = runFormattingLint(t, dir)
			require.NoError(t, err, "formatted fixture must pass: %s", output)
		})
	}
}

func formattingGateFixture(t *testing.T, filename, source string) string {
	t.Helper()
	dir := t.TempDir()
	makefile, err := os.ReadFile(filepath.Join(projectRoot(t), "Makefile"))
	require.NoError(t, err)
	files := map[string]string{
		"Makefile":          string(makefile),
		"go.mod":            "module fixture\n\ngo 1.26.0\n",
		"base.go":           "package fixture\n",
		"bin/golangci-lint": "#!/bin/sh\nexit 0\n",
		filename:            source,
	}
	for path, raw := range files {
		full := filepath.Join(dir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
		require.NoError(t, os.WriteFile(full, []byte(raw), 0o600))
	}
	require.NoError(t, os.Chmod(filepath.Join(dir, "bin/golangci-lint"), 0o700))
	return dir
}

func runFormattingLint(t *testing.T, dir string) ([]byte, error) {
	t.Helper()
	command := exec.Command("make", "lint-go", "GO_PKGS=./...")
	command.Dir = dir
	command.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return command.CombinedOutput()
}

// Parser errors must fail closed; excluded dependencies and metadata are outside
// the project source inventory. These cases do not contribute to compiled RED.
func TestLintGo_FormattingInventory_ExcludesMetadataAndFailsClosed(t *testing.T) {
	for _, excluded := range []string{".git", ".mgit", ".hyperswe", ".cache", "vendor", "node_modules"} {
		t.Run(excluded, func(t *testing.T) {
			dir := formattingGateFixture(t, excluded+"/ignored.go", "invalid Go source\n")
			output, err := runFormattingLint(t, dir)
			require.NoError(t, err, "excluded directory: %s", output)
		})
	}
	t.Run("parser error", func(t *testing.T) {
		dir := formattingGateFixture(t, "broken.go", "package fixture\nfunc {\n")
		output, err := runFormattingLint(t, dir)
		require.Error(t, err)
		require.Contains(t, string(output), "broken.go")
	})
}
