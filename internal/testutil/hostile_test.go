// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestHostileIDs_CoversEveryClass(t *testing.T) {
	seen := map[HostileClass]int{}
	for _, f := range HostileIDs() {
		require.NotEmpty(t, f.Name)
		require.NotEmpty(t, f.Class, f.Name)
		seen[f.Class]++
	}
	require.NotEmpty(t, AllHostileClasses())
	for _, c := range AllHostileClasses() {
		require.NotZero(t, seen[c], "class %s has no HostileIDs fixture", c)
	}
	for c := range seen {
		require.Contains(t, AllHostileClasses(), c)
	}
	// Spot-check payload semantics so a class cannot be mislabelled.
	byName := map[string]string{}
	for _, f := range HostileIDs() {
		byName[f.Name] = f.Value
	}
	require.Contains(t, byName[firstOfClass(t, ClassNUL)], "\x00")
	require.False(t, utf8.ValidString(byName[firstOfClass(t, ClassInvalidUTF8)]))
	require.Contains(t, byName[firstOfClass(t, ClassESC)], "\x1b")
	require.Contains(t, byName[firstOfClass(t, ClassTraversal)], "..")
	require.Contains(t, byName[firstOfClass(t, ClassScript)], "<script>")
}

func firstOfClass(t *testing.T, c HostileClass) string {
	t.Helper()
	for _, f := range HostileIDs() {
		if f.Class == c {
			return f.Name
		}
	}
	t.Fatalf("no fixture of class %s", c)
	return ""
}

func TestHostileIDs_AreStableAndDistinct(t *testing.T) {
	first := HostileIDs()
	names := map[string]bool{}
	values := map[string]bool{}
	for _, f := range first {
		require.False(t, names[f.Name], "duplicate name %q", f.Name)
		require.False(t, values[f.Value], "duplicate value for %q", f.Name)
		require.NotEmpty(t, f.Value, f.Name)
		names[f.Name], values[f.Value] = true, true
	}
	// Mutate the returned slice and a fixture; later calls must be unaffected.
	first[0].Value = "mutated"
	first[0].Name = "mutated"
	second := HostileIDs()
	third := HostileIDs()
	require.Equal(t, second, third)
	require.NotEqual(t, "mutated", second[0].Value)
	require.Len(t, second, len(first))
}

func TestHostileText_Has64KiBFixture(t *testing.T) {
	found := false
	for _, f := range HostileText() {
		if f.Class == ClassLong && len(f.Value) > 64*1024 {
			found = true
		}
	}
	require.True(t, found)
	seen := map[HostileClass]bool{}
	for _, f := range HostileText() {
		seen[f.Class] = true
	}
	for _, c := range AllHostileClasses() {
		require.True(t, seen[c], "class %s has no HostileText fixture", c)
	}
	require.Equal(t, HostileText(), HostileText())
}

func TestHostileIDs_LongFixtureExceeds64KiB(t *testing.T) {
	for _, f := range HostileIDs() {
		if f.Class == ClassLong {
			require.Greater(t, len(f.Value), 64*1024)
			return
		}
	}
	t.Fatal("no long ID fixture")
}

func TestHostileFixtures_ImportStdlibOnly(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "hostile.go", nil, parser.ImportsOnly)
	require.NoError(t, err)
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		require.NoError(t, err)
		require.False(t, strings.Contains(strings.Split(p, "/")[0], "."), "non-stdlib import %s", p)
	}
}

func TestTestutil_NoProductionImporters(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, dir), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			if strings.Contains(filepath.ToSlash(p), "/internal/testutil/") {
				return nil
			}
			src, rerr := os.ReadFile(p) //nolint:gosec // G304: walking the repo's own source files in a test
			if rerr != nil {
				return rerr
			}
			if strings.Contains(string(src), "mtix/internal/testutil\"") {
				offenders = append(offenders, p)
			}
			return nil
		})
		require.NoError(t, err)
	}
	require.Empty(t, offenders)
}
