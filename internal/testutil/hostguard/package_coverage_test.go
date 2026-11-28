// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hostguard_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const oplocalSuffix = "/internal/oplocal"

type listedPackage struct {
	dir     string
	imports []string
}

// listPackages returns every package (and test variant) of the repository,
// including the e2e and faultinject build tags, with its direct imports.
func listPackages(t *testing.T, repo string) map[string]listedPackage {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := runGoList(goTool, repo)
	require.NoError(t, err)
	pkgs := map[string]listedPackage{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		var imports []string
		if parts[2] != "" {
			imports = strings.Split(parts[2], ",")
		}
		pkgs[parts[0]] = listedPackage{dir: parts[1], imports: imports}
	}
	return pkgs
}

// runGoList lists the repository's packages. A failure carries go's stderr
// and the environment the walk assumes, so a box-only failure is diagnosable.
func runGoList(goTool, repo string) ([]byte, error) {
	cmd := exec.Command(goTool, "list", "-test", "-deps", "-tags", "e2e faultinject",
		"-f", "{{.ImportPath}}|{{.Dir}}|{{join .Imports \",\"}}", "./...")
	cmd.Dir = repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list in %s failed (needs a resolvable module graph; environment: "+
			"GOFLAGS=%q GOPROXY=%q GOMODCACHE=%q GOTOOLCHAIN=%q): %w\nstderr: %s",
			repo, os.Getenv("GOFLAGS"), os.Getenv("GOPROXY"), os.Getenv("GOMODCACHE"),
			os.Getenv("GOTOOLCHAIN"), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// zeroCheckedMessage explains an empty walk as an environment problem.
func zeroCheckedMessage(repo string) string {
	return "no test binary reached oplocal: the graph walk is broken; go list under " + repo +
		" (-tags \"e2e faultinject\") returned no repository test binaries; check GOFLAGS, " +
		"the repository path and build tags"
}

func reachesOplocal(pkgs map[string]listedPackage, start string) bool {
	seen := map[string]bool{}
	stack := []string{start}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if strings.HasSuffix(strings.SplitN(cur, " ", 2)[0], oplocalSuffix) {
			return true
		}
		stack = append(stack, pkgs[cur].imports...)
	}
	return false
}

func dirInstallsGuard(t *testing.T, dir string) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	require.NoError(t, err)
	for _, f := range files {
		body, readErr := os.ReadFile(f)
		require.NoError(t, readErr)
		if strings.Contains(string(body), "hostguard.Run(") {
			return true
		}
	}
	return false
}

// TestTestBinaries_ReachingOplocal_InstallTheGuard walks the import graph of
// every test binary in the repository (default, e2e and faultinject tags). A
// test binary that can reach internal/oplocal, and so resolve the real user
// config or home, must install hostguard.Run in a TestMain (MTIX-112.39).
func TestTestBinaries_ReachingOplocal_InstallTheGuard(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	pkgs := listPackages(t, repo)
	var missing []string
	checked := 0
	for path, pkg := range pkgs {
		if !strings.HasSuffix(path, ".test") || !strings.HasPrefix(pkg.dir, repo) {
			continue
		}
		if strings.Contains(pkg.dir, string(filepath.Separator)+"node_modules"+string(filepath.Separator)) || !reachesOplocal(pkgs, path) {
			continue
		}
		checked++
		if !dirInstallsGuard(t, pkg.dir) {
			missing = append(missing, strings.TrimPrefix(pkg.dir, repo+string(filepath.Separator)))
		}
	}
	sort.Strings(missing)
	require.NotZero(t, checked, zeroCheckedMessage(repo))
	require.Empty(t, missing, "test packages that reach internal/oplocal but have no TestMain calling hostguard.Run")
}
