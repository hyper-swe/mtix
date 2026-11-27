// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Raw opens are allowed only in these functions, once each. None hands a
// query-bearing handle to its caller. Test fixtures are excluded by the scan.
func rawOpenReasons() map[string]string {
	return map[string]string{
		"dsn.go:openSQLite":                          "definition of the guarded raw opener; no queries",
		"dsn.go:openShieldedSQLite":                  "driver probe only; closed without connecting or querying",
		"backup.go:verifyDatabase":                   "one quick_check, then close on success or error; no later read",
		"inbox_history.go:countUnackedRO":            "one count, then close; immutable fallback opens a different handle",
		"attach_metadata.go:readStoreInstanceIDOnce": "one identity query, then close; each retry opens a new handle",
		"import_stage.go:openStage":                  "private scratch staging database of one import, single connection, deleted on Close; never a store or reader pool (MTIX-2.3.2.1)",
	}
}

// rawOpenBypasses checks identifiers, so passing or aliasing the opener is
// covered as well as calls. File-wide exemptions would hide new unsafe uses.
func rawOpenBypasses(fset *token.FileSet, name string, file *ast.File) []string {
	var out []string
	reasons := rawOpenReasons()
	for _, decl := range file.Decls {
		key := filepath.Base(name) + ":"
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
			key += fn.Name.Name
		}
		count := 0
		ast.Inspect(decl, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || id.Name != "openSQLite" {
				return true
			}
			count++
			if reasons[key] == "" || count > 1 {
				out = append(out, name+":"+strconv.Itoa(fset.Position(id.Pos()).Line)+": unshielded opener outside documented single-use allowance")
			}
			return true
		})
	}
	return out
}

// Guard mutations cover all files, function-scoped exceptions and aliases.
func TestRawOpenGuard_PackageWideReferences_RequireReason(t *testing.T) {
	cases := []struct {
		file, src string
		want      int
	}{
		{"new.go", `func f() { openSQLite("db") }`, 1},
		{"recover.go", `func f() { openSQLite("db") }`, 1},
		{"recover_inbox.go", `func f() { use(openSQLite) }`, 1},
		{"attach_metadata.go", `var opener = openSQLite`, 1},
		{"backup.go", `func other() { openSQLite("db") }`, 1},
		{"dsn.go", `func other() { openSQLite("db") }`, 1},
		{"backup.go", `func verifyDatabase() { openSQLite("db") }`, 0},
		{"backup.go", `func verifyDatabase() { openSQLite("db"); openSQLite("db") }`, 1},
		{"backup.go", `func (s Store) verifyDatabase() { openSQLite("db") }`, 1},
		{"new.go", `func f() { openShieldedSQLite("db") }`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.file+tc.src, func(t *testing.T) {
			files := map[string]string{tc.file: "package sqlite\n" + tc.src}
			for i := 0; i < readPoolGuardMinFiles; i++ {
				files["f"+strconv.Itoa(i)+".go"] = "package sqlite"
			}
			got, err := readPoolBypasses(files)
			require.NoError(t, err)
			require.Len(t, got, tc.want)
		})
	}
}
