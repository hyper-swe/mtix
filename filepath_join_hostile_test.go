// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Guards filepath.Join operands across production Go sources (MTIX-187.7.7).
package mtix_test

import (
	"fmt"
	"go/ast"
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

// TestFilepathJoin_NoIDOrTitleOperands fails when production Go code passes an
// ID or title operand to filepath.Join.
//
// Limits: the guard is syntactic and name based. It matches identifiers and
// selectors named id, title, or ending in ID, Title, _id or _title. It does not
// detect a lower-case "Id" suffix, an ID copied into a differently named
// variable before the Join, path.Join, string concatenation or fmt.Sprintf
// results passed to os calls, or taint through helper functions. The runtime
// tests in the writer packages cover those paths for the writers they drive.
func TestFilepathJoin_NoIDOrTitleOperands(t *testing.T) {
	// Peer IDs are protocol identities, not opaque event IDs or ticket titles.
	// tick.New validates the grammar before joining; doctor only renders paths.
	allow := map[string]string{
		"internal/relay/tick/stack.go:cfg.PeerID":   "validated FR-21 peer grammar before mkdir",
		"cmd/mtix/sync_doctor_relay.go:peer.PeerID": "validated relay peer inventory; diagnostic path only",
	}
	violations, stale, err := scanJoinOperands(".", allow)
	require.NoError(t, err)
	require.Empty(t, violations)
	require.Empty(t, stale, "remove stale allowlist entries")
}

// scanJoinOperands walks production Go files under root and returns the raw
// ID/title Join operands that are not allowlisted, and the allowlist entries
// that matched nothing or carry no reason.
func scanJoinOperands(root string, allow map[string]string) (violations, stale []string, err error) {
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if e.IsDir() {
			if e.Name() == "node_modules" || strings.HasPrefix(e.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found, scanErr := scanJoinFile(root, path, allow, seen)
		violations = append(violations, found...)
		return scanErr
	})
	for key, reason := range allow {
		if !seen[key] || reason == "" {
			stale = append(stale, key)
		}
	}
	return violations, stale, err
}

// scanJoinFile reports the non-allowlisted ID/title Join operands of one file.
func scanJoinFile(root, path string, allow map[string]string, seen map[string]bool) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, operand := range idTitleJoins(file) {
		key := filepath.ToSlash(rel) + ":" + operand.name
		if reason, ok := allow[key]; ok && reason != "" {
			seen[key] = true
		} else {
			violations = append(violations, fmt.Sprintf("%s: raw ID/title operand %s in filepath.Join", fset.Position(operand.pos), operand.name))
		}
	}
	return violations, nil
}

func TestFilepathJoin_ScanReportsStaleAndUnlistedEntries(t *testing.T) {
	root := t.TempDir()
	src := "package p\nimport \"path/filepath\"\nvar a = filepath.Join(\"b\", ev.ID)\nvar c = filepath.Join(\"b\", \"fixed\")\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "p.go"), []byte(src), 0o600))
	for _, tc := range []struct {
		name           string
		allow          map[string]string
		violations, st int
	}{
		{"unlisted operand is a violation", map[string]string{}, 1, 0},
		{"listed operand with reason passes", map[string]string{"p.go:ev.ID": "audited"}, 0, 0},
		{"listed operand without reason is stale and a violation", map[string]string{"p.go:ev.ID": ""}, 1, 1},
		{"entry matching nothing is stale", map[string]string{"p.go:ev.ID": "audited", "gone.go:x.ID": "audited"}, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			violations, stale, err := scanJoinOperands(root, tc.allow)
			require.NoError(t, err)
			require.Len(t, violations, tc.violations)
			require.Len(t, stale, tc.st)
		})
	}
}

type pathOperand struct {
	name string
	pos  token.Pos
}

func idTitleJoins(file *ast.File) []pathOperand {
	aliases := map[string]bool{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "path/filepath" {
			continue
		}
		alias := "filepath"
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		aliases[alias] = true
	}
	var found []pathOperand
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isFilepathJoin(call.Fun, aliases) {
			return true
		}
		for _, arg := range call.Args {
			found = append(found, idTitleOperands(arg)...)
		}
		return true
	})
	return found
}

func isFilepathJoin(expr ast.Expr, aliases map[string]bool) bool {
	switch fn := expr.(type) {
	case *ast.Ident:
		return aliases["."] && fn.Name == "Join"
	case *ast.SelectorExpr:
		pkg, ok := fn.X.(*ast.Ident)
		return ok && aliases[pkg.Name] && fn.Sel.Name == "Join"
	}
	return false
}

func idTitleOperands(expr ast.Expr) []pathOperand {
	var found []pathOperand
	ast.Inspect(expr, func(n ast.Node) bool {
		switch value := n.(type) {
		case *ast.SelectorExpr:
			if idTitleName(value.Sel.Name) {
				owner, ok := value.X.(*ast.Ident)
				name := value.Sel.Name
				if ok {
					name = owner.Name + "." + name
				}
				found = append(found, pathOperand{name, value.Pos()})
				return false
			}
			return true
		case *ast.Ident:
			if idTitleName(value.Name) {
				found = append(found, pathOperand{value.Name, value.Pos()})
			}
		}
		return true
	})
	return found
}

func idTitleName(name string) bool {
	return name == "id" || name == "title" || strings.HasSuffix(name, "ID") ||
		strings.HasSuffix(name, "Title") || strings.HasSuffix(name, "_id") || strings.HasSuffix(name, "_title")
}

func TestFilepathJoin_GuardRecognizesOperands(t *testing.T) {
	for _, tc := range []struct {
		alias, expression string
		want              int
	}{
		{"filepath", `filepath.Join(base, event.ID)`, 1},
		{"fp", `fp.Join(base, ticket.Title+".txt")`, 1},
		{".", `Join(base, filepathBase(event.ID))`, 1},
		{"filepath", `filepath.Join(base, eventID)`, 1},
		{"filepath", `filepath.Join(base, event.ID.Value)`, 1},
		{"filepath", `filepath.Join(base, "tasks.json")`, 0},
		{"filepath", `other.Join(base, event.ID)`, 0},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			source := fmt.Sprintf("package fixture; import %s %q; var path = %s", tc.alias, "path/filepath", tc.expression)
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
			require.NoError(t, err)
			require.Len(t, idTitleJoins(file), tc.want)
		})
	}
}
