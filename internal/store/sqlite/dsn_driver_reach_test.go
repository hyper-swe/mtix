// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	// attachSQLRe matches ATTACH as SQL text: ATTACH DATABASE, or
	// ATTACH <any expression> AS <name>. Prose such as "drain attach queue"
	// does not match.
	attachSQLRe = regexp.MustCompile(`(?i)\battach\s+database\b|\battach\s+[^;]+?\s+as\s`)
	// vacuumIntoRe matches VACUUM INTO as SQL text, with or without a schema
	// name between VACUUM and INTO.
	vacuumIntoRe = regexp.MustCompile(`(?i)\bvacuum\s+(\S+\s+)?into\b`)
)

// isDriverImport reports the imports whose Open, OpenDB or Driver reach a
// SQLite connection without openSQLite: the SQLite driver and database/sql/driver.
func isDriverImport(importPath string) bool {
	return importPath == "database/sql/driver" || importPath == "modernc.org/sqlite" || strings.HasPrefix(importPath, "modernc.org/sqlite/")
}

// sqliteErrorFiles lists, by exact repository path, the files that may import
// modernc.org/sqlite under a name: they only read its Error type to classify
// failures. driverRegistrationFile may import it blank, to register the driver.
var sqliteErrorFiles = map[string]bool{
	"internal/store/sqlite/open_retry.go": true,
	"internal/store/sqlite/durability.go": true,
}

const driverRegistrationFile = "internal/store/sqlite/store.go"

// sqliteImportAllowed reports whether a modernc.org/sqlite import under the
// local name local is allowed in the file at filePath.
func sqliteImportAllowed(filePath, local string) bool {
	p := filepath.ToSlash(filePath)
	return sqliteErrorFiles[p] || (local == "_" && p == driverRegistrationFile)
}

// shieldDriverFiles lists, by exact repository path, the files that may name
// the driver value: the reader pool wraps it in a connector that shields every
// query from a cancelled context (MTIX-250). dsn.go takes the driver from the
// pool it opened through the helper; read_shield.go holds it as a driver.Driver.
// Open, OpenDB and every other reach stay forbidden in them.
var shieldDriverFiles = map[string]bool{
	"internal/store/sqlite/dsn.go":         true,
	"internal/store/sqlite/read_shield.go": true,
}

// backupFile and backupFunc are the one place VACUUM INTO text may appear.
const (
	backupFile = "internal/store/sqlite/backup.go"
	backupFunc = "Store.Backup"
)

// funcName returns a declaration's name as "Recv.Name" or "Name".
func funcName(decl *ast.FuncDecl) string {
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return decl.Name.Name
	}
	typ := decl.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + decl.Name.Name
	}
	return decl.Name.Name
}

// driverReaches parses one non-test source and lists the other ways it can
// open or reset a SQLite database: any Open, OpenDB or Driver taken from the
// SQLite driver or database/sql/driver import under any local name, a
// selector named Driver, NewBackup or NewRestore (called or not), an import of
// the SQLite driver outside the error-type files, SQL text that ATTACHes a
// database, and SQL text VACUUM [schema] INTO outside Store.Backup in
// backup.go. A dot import of those
// packages and an unparseable source are reported too.
func driverReaches(filePath, text string) []string {
	file, err := parser.ParseFile(token.NewFileSet(), filePath, text, parser.SkipObjectResolution)
	if err != nil {
		return nil // fileViolations reports the unparseable source once
	}
	names := map[string]bool{}
	var out []string
	for _, imp := range file.Imports {
		importPath, _ := strconv.Unquote(imp.Path.Value)
		if !isDriverImport(importPath) {
			continue
		}
		local := path.Base(importPath)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if local == "." {
			out = append(out, filePath+": dot import of "+importPath+" hides its Open")
		}
		if importPath != "database/sql/driver" && !sqliteImportAllowed(filePath, local) {
			out = append(out, filePath+": "+importPath+" imported outside the error-type files")
		}
		names[local] = true
	}
	scan := func(node ast.Node, fn string) {
		out = append(out, nodeReaches(filePath, node, fn, names)...)
	}
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			scan(fd, funcName(fd))
		} else {
			scan(decl, "")
		}
	}
	return out
}

// nodeReaches judges the nodes under one declaration; fn is its function.
func nodeReaches(where string, root ast.Node, fn string, driverNames map[string]bool) []string {
	var out []string
	ast.Inspect(root, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			shield := v.Sel.Name == "Driver" && shieldDriverFiles[filepath.ToSlash(where)]
			if shield {
				return true
			}
			if id, ok := v.X.(*ast.Ident); ok && driverNames[id.Name] && isDriverReach(v.Sel.Name) {
				out = append(out, where+": driver reached without the helper ("+id.Name+"."+v.Sel.Name+")")
			} else if isAnyReceiverReach(v.Sel.Name) {
				out = append(out, where+": driver reached without the helper (a "+v.Sel.Name+" selector)")
			}
		case *ast.BasicLit:
			out = append(out, literalReaches(where, v, fn)...)
		}
		return true
	})
	return out
}

func isDriverReach(name string) bool {
	return name == "Open" || name == "OpenDB" || name == "Driver"
}

// isAnyReceiverReach reports the selectors that reach a SQLite connection
// whatever they are taken from, called or held as a method value: Driver
// (a *sql.DB's driver) and NewBackup or NewRestore (the driver's backup API).
func isAnyReceiverReach(name string) bool {
	return name == "Driver" || name == "NewBackup" || name == "NewRestore"
}

// literalReaches judges one string literal: ATTACH anywhere, VACUUM INTO
// anywhere but Store.Backup in backup.go.
func literalReaches(where string, lit *ast.BasicLit, fn string) []string {
	if lit.Kind != token.STRING {
		return nil
	}
	text, err := strconv.Unquote(lit.Value)
	if err != nil {
		text = lit.Value
	}
	var out []string
	if attachSQLRe.MatchString(text) {
		out = append(out, where+": ATTACH in SQL text opens a second database file")
	}
	if vacuumIntoRe.MatchString(text) && (filepath.ToSlash(where) != backupFile || fn != backupFunc) {
		out = append(out, where+": VACUUM INTO outside "+backupFunc+" in "+backupFile)
	}
	return out
}
