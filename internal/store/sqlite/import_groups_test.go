// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestImportGroups_BackfillEmit_StdlibThenInternal verifies that the import
// block of sync_backfill_emit.go places standard-library paths in their own
// group ahead of module paths, with each group sorted.
func TestImportGroups_BackfillEmit_StdlibThenInternal(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "sync_backfill_emit.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Split imports into groups separated by blank lines.
	var groups [][]string
	prevLine := -1
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("unquote %s: %v", imp.Path.Value, err)
		}
		line := fset.Position(imp.Pos()).Line
		if prevLine < 0 || line > prevLine+1 {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], path)
		prevLine = line
	}

	isStdlib := func(p string) bool {
		first := strings.SplitN(p, "/", 2)[0]
		return !strings.Contains(first, ".")
	}

	seenModule := false
	for gi, g := range groups {
		std := isStdlib(g[0])
		for _, p := range g {
			if isStdlib(p) != std {
				t.Errorf("group %d mixes standard-library and module paths: %v", gi, g)
				break
			}
		}
		if std && seenModule {
			t.Errorf("standard-library group %d follows a module path group: %v", gi, g)
		}
		if !std {
			seenModule = true
		}
		if !sort.StringsAreSorted(g) {
			t.Errorf("group %d is not sorted: %v", gi, g)
		}
	}
}
