// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package mtix_test contains project-level scaffolding verification tests.
// These tests validate MTIX-1.2.1 (go.mod), MTIX-1.2.2 (directory structure),
// MTIX-1.2.3 (Makefile), and MTIX-1.2.4 (.golangci.yml).
package mtix_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// projectRoot returns the absolute path to the project root.
func projectRoot(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "failed to get caller info")
	return filepath.Dir(filename)
}

// TestGoMod_Exists verifies go.mod exists at project root (MTIX-1.2.1).
func TestGoMod_Exists(t *testing.T) {
	root := projectRoot(t)
	goModPath := filepath.Join(root, "go.mod")
	_, err := os.Stat(goModPath)
	assert.NoError(t, err, "go.mod should exist at project root")
}

// TestGoMod_CorrectModulePath verifies the module path is correct (MTIX-1.2.1).
func TestGoMod_CorrectModulePath(t *testing.T) {
	root := projectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)

	assert.Contains(t, string(content), "module github.com/hyper-swe/mtix",
		"go.mod should declare correct module path")
}

// TestGoMod_AllApprovedDepsPresent verifies all approved dependencies
// are listed in go.mod (MTIX-1.2.1).
func TestGoMod_AllApprovedDepsPresent(t *testing.T) {
	root := projectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)

	goMod := string(content)

	// Only check dependencies that are actually used in the codebase.
	// Viper is approved but not used (we use StaticConfig instead).
	approvedDeps := []string{
		"modernc.org/sqlite",
		"github.com/spf13/cobra",
		"github.com/gorilla/websocket",
		"google.golang.org/grpc",
		"google.golang.org/protobuf",
		"github.com/stretchr/testify",
		"github.com/oklog/ulid/v2",
	}

	for _, dep := range approvedDeps {
		assert.Contains(t, goMod, dep,
			"go.mod should contain approved dependency: %s", dep)
	}
}

// TestGoMod_GoVersionAtLeast122 verifies the Go version directive is 1.22+.
func TestGoMod_GoVersionAtLeast122(t *testing.T) {
	root := projectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)

	goMod := string(content)

	// Check for go 1.22 or higher using a more flexible approach.
	// Extract the go version line and parse the version number.
	lines := strings.Split(goMod, "\n")
	var goVersionLine string
	for _, line := range lines {
		if strings.HasPrefix(line, "go ") {
			goVersionLine = line
			break
		}
	}

	assert.NotEmpty(t, goVersionLine, "go.mod should contain a 'go' version directive")

	// Check that version is at least 1.22.
	// Valid formats: "go 1.22", "go 1.22.0", "go 1.23", "go 1.25.0", etc.
	hasValidGoVersion := strings.HasPrefix(goVersionLine, "go 1.22") ||
		strings.HasPrefix(goVersionLine, "go 1.23") ||
		strings.HasPrefix(goVersionLine, "go 1.24") ||
		strings.HasPrefix(goVersionLine, "go 1.25") ||
		strings.HasPrefix(goVersionLine, "go 1.26") ||
		strings.HasPrefix(goVersionLine, "go 2.")
	assert.True(t, hasValidGoVersion, "go.mod should specify Go 1.22+ (found: %s)", goVersionLine)
}

// TestProjectStructure_AllDirectoriesExist verifies all required directories
// from REQUIREMENTS.md Section 6 exist (MTIX-1.2.2).
func TestProjectStructure_AllDirectoriesExist(t *testing.T) {
	root := projectRoot(t)

	requiredDirs := []string{
		"cmd/mtix",
		"internal/model",
		"internal/store/sqlite",
		"internal/service",
		"internal/api/http",
		"internal/api/grpc",
		"internal/mcp",
		"internal/docs/templates",
		"internal/testutil",
		"internal/integration",
		"proto/mtix/v1",
		"web/src/components",
		"web/src/hooks",
		"web/public",
		"sdk/python/mtix",
		"e2e",
		"docs",
	}

	for _, dir := range requiredDirs {
		dirPath := filepath.Join(root, dir)
		info, err := os.Stat(dirPath)
		if assert.NoError(t, err, "directory %s should exist", dir) {
			assert.True(t, info.IsDir(), "%s should be a directory", dir)
		}
	}
}

// TestMakefile_Exists verifies Makefile exists at project root (MTIX-1.2.3).
func TestMakefile_Exists(t *testing.T) {
	root := projectRoot(t)
	_, err := os.Stat(filepath.Join(root, "Makefile"))
	assert.NoError(t, err, "Makefile should exist at project root")
}

// TestMakefile_HasRequiredTargets verifies all required build targets (MTIX-1.2.3).
func TestMakefile_HasRequiredTargets(t *testing.T) {
	root := projectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)

	makefile := string(content)

	requiredTargets := []string{
		"build:",
		"test:",
		"test-race:",
		"test-cover:",
		"lint:",
		"security-scan:",
		"bench:",
		"clean:",
		"verify:",
	}

	for _, target := range requiredTargets {
		assert.Contains(t, makefile, target,
			"Makefile should contain target: %s", target)
	}
}

// TestGolangciConfig_Exists verifies .golangci.yml exists (MTIX-1.2.4).
func TestGolangciConfig_Exists(t *testing.T) {
	root := projectRoot(t)
	_, err := os.Stat(filepath.Join(root, ".golangci.yml"))
	assert.NoError(t, err, ".golangci.yml should exist at project root")
}

// TestGolangciConfig_AllRequiredLintersEnabled verifies required linters (MTIX-1.2.4).
func TestGolangciConfig_AllRequiredLintersEnabled(t *testing.T) {
	root := projectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	require.NoError(t, err)

	config := string(content)

	requiredLinters := []string{
		"gosec",
		"govet",
		"staticcheck",
		"errcheck",
		"gocritic",
		"ineffassign",
		"misspell",
		"gocyclo",
		"gocognit",
	}

	for _, linter := range requiredLinters {
		assert.Contains(t, config, linter,
			".golangci.yml should enable linter: %s", linter)
	}
}

// TestGolangciConfig_ComplexityLimitsSet verifies complexity limits (MTIX-1.2.4).
func TestGolangciConfig_ComplexityLimitsSet(t *testing.T) {
	root := projectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	require.NoError(t, err)

	config := string(content)

	// Verify cyclomatic complexity limit of 15.
	assert.Contains(t, config, "min-complexity: 15",
		"gocyclo should be set to max complexity 15")

	// Verify cognitive complexity limit of 20.
	assert.Contains(t, config, "min-complexity: 20",
		"gocognit should be set to max complexity 20")
}
