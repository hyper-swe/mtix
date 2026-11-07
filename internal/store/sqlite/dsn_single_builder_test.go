// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	// otherOpenRe matches the ways to reach the driver itself without the
	// helper: a driver value's own Open. The database/sql selectors (Open,
	// OpenDB) are found from the syntax tree, whatever the import is named.
	otherOpenRe    = regexp.MustCompile(`\bsqlite3?\.(Driver|Open)\b|\bDriver\{\}\.Open\(`)
	dsnLiteralRe   = regexp.MustCompile(`"file:"\s*\+|"\?(mode|_pragma|_txlock|immutable|cache)=|\+\s*"\?`)
	sprintfDSNRe   = regexp.MustCompile(`Sprintf\(\s*["` + "`" + `][^"` + "`" + `]*(file:|\?(mode|_pragma|_txlock|immutable|cache)=)`)
	vacuumLiteralR = regexp.MustCompile(`VACUUM INTO\s*'|VACUUM INTO[^"` + "`" + `\n]*%`)
)

// dsnGuardMinFiles is the least number of non-test Go files a scan must see.
// Fewer means the walk matched the wrong tree, and a guard that scanned
// nothing would pass for the wrong reason.
const dsnGuardMinFiles = 100

// nonSQLiteOpeners lists the files, by exact repository path, that hold a
// database/sql Open for a database other than SQLite. The scan skips their
// opens but still fails if such a file ever names sqlite. tools/cleanup-test-schemas.go
// is a Postgres schema cleanup tool (build tag cleanup) whose driver name is a
// flag defaulting to postgres; it keeps sql.Open in a variable for its tests.
var nonSQLiteOpeners = map[string]bool{"tools/cleanup-test-schemas.go": true}

// sqlOpenUses parses one Go source and returns how many selectors Open and
// OpenDB it takes from the database/sql import, whatever local name the
// import has, whether called or used as a value (var f = sql.Open). A dot
// import or a source that does not parse is reported in problems, because the
// scan could not then be sure of what it counted.
func sqlOpenUses(path, text string) (opens, openDBs int, problems []string) {
	file, err := parser.ParseFile(token.NewFileSet(), path, text, parser.SkipObjectResolution)
	if err != nil {
		return 0, 0, []string{path + ": cannot parse, so the open scan cannot judge it"}
	}
	names := map[string]bool{}
	for _, imp := range file.Imports {
		if imp.Path.Value != `"database/sql"` {
			continue
		}
		local := "sql"
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if local == "." {
			problems = append(problems, path+": dot import of database/sql hides its Open")
		}
		names[local] = true
	}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, isID := sel.X.(*ast.Ident); isID && names[id.Name] {
			switch sel.Sel.Name {
			case "Open":
				opens++
			case "OpenDB":
				openDBs++
			}
		}
		return true
	})
	return opens, openDBs, problems
}

// dsnViolations scans non-test Go sources (path -> text) and returns one
// line per breach of the single-builder rule (MTIX-112.2.4): the only
// database/sql Open (found in the syntax tree under any import name, called or
// held as a value, with any driver argument) is in dsn.go; no DSN or query
// string is spelled out anywhere else; VACUUM INTO never takes its target as a
// literal. It errors when it was given too few files to mean anything.
func dsnViolations(files map[string]string, minFiles int) ([]string, error) {
	if len(files) < minFiles {
		return nil, fmt.Errorf("dsn guard scanned %d files, need at least %d: the walk matched nothing", len(files), minFiles)
	}
	var out []string
	opens := 0
	for path, text := range files {
		n, found := fileViolations(path, text)
		opens += n
		out = append(out, found...)
	}
	if opens != 1 {
		out = append(out, fmt.Sprintf("expected exactly one sql.Open(\"sqlite\") in dsn.go, found %d", opens))
	}
	sort.Strings(out)
	return out, nil
}

// fileViolations judges one non-test source: it returns how many database/sql
// Open selectors it holds (counted toward the single allowed one) and the
// breaches it commits.
func fileViolations(path, text string) (int, []string) {
	n, openDBs, out := sqlOpenUses(path, text)
	if nonSQLiteOpeners[filepath.ToSlash(path)] {
		if strings.Contains(strings.ToLower(text), "sqlite") {
			out = append(out, path+": listed as a non-SQLite opener but names sqlite")
		}
		return 0, append(out, driverReaches(path, text)...)
	}
	src := dropCommentLines(text)
	isHelper := filepath.Base(path) == "dsn.go" && strings.Contains(filepath.ToSlash(path), "internal/store/sqlite/")
	if n > 0 && !isHelper {
		out = append(out, path+": sql.Open outside dsn.go")
	}
	if !isHelper && (openDBs > 0 || otherOpenRe.MatchString(src)) {
		out = append(out, path+": driver reached without the helper (sql.OpenDB or driver Open)")
	}
	if !isHelper && sprintfDSNRe.MatchString(src) {
		out = append(out, path+": DSN built with Sprintf")
	}
	if !isHelper && dsnLiteralRe.MatchString(src) {
		out = append(out, path+": DSN or query string built by hand")
	}
	if vacuumLiteralR.MatchString(src) {
		out = append(out, path+": VACUUM INTO target is not a bound parameter")
	}
	return n, append(out, driverReaches(path, text)...)
}

// dropCommentLines removes whole-line // comments so prose that names the
// forbidden forms does not count as code.
func dropCommentLines(src string) string {
	var keep []string
	for _, line := range strings.Split(src, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

func readNonTestGoFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".hyperswe", ".codex-lane", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			b, rerr := os.ReadFile(p) //nolint:gosec // G304: a path found by walking this repository's own tree.
			if rerr != nil {
				return rerr
			}
			rel, _ := filepath.Rel(root, p)
			files[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	}))
	return files
}

// TestSQLiteDSN_NonTestSourcesUseOneBuilder fails when any non-test Go file
// in the module opens SQLite or builds a DSN other than through dsn.go.
func TestSQLiteDSN_NonTestSourcesUseOneBuilder(t *testing.T) {
	files := readNonTestGoFiles(t, filepath.Join("..", "..", ".."))
	got, err := dsnViolations(files, dsnGuardMinFiles)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Contains(t, files, "internal/store/sqlite/dsn.go")
}

// plantedSource wraps a statement list into a parseable Go file that imports
// database/sql under the given spec (for example `"database/sql"` or
// `d "database/sql"`).
func plantedSource(importSpec, body string) string {
	return "package x\n\nimport " + importSpec + "\n\nfunc f() {\n" + body + "\n}\n"
}

// TestDSNViolations_PlantedBreaches_AreCaught proves the scanner fails on
// each planted violation, and on a scan that saw too little to judge.
func TestDSNViolations_PlantedBreaches_AreCaught(t *testing.T) {
	helper := "internal/store/sqlite/dsn.go"
	std := `"database/sql"`
	clean := map[string]string{
		helper:   plantedSource(std, `db, err := sql.Open("sqlite", sqliteDSN(p))`),
		"a/b.go": "package b",
	}
	cases := []struct{ name, file, src, want string }{
		{"second open", "x/open.go", plantedSource(std, `sql.Open("sqlite", d)`), "outside dsn.go"},
		{"aliased import open", "x/al.go", plantedSource(`d "database/sql"`, `d.Open("sqlite", x)`), "outside dsn.go"},
		{"odd alias open", "x/al2.go", plantedSource(`mydb "database/sql"`, `mydb.Open(name, x)`), "outside dsn.go"},
		{"open held as a value", "x/val.go", plantedSource(std, `open0 := sql.Open`), "outside dsn.go"},
		{"open held as a package value", "x/pv.go", "package x\n\nimport \"database/sql\"\n\nvar open0 = sql.Open\n", "outside dsn.go"},
		{"aliased open held as a value", "x/av.go", "package x\n\nimport q \"database/sql\"\n\nvar open0 = q.Open\n", "outside dsn.go"},
		{"dot import", "x/dot.go", plantedSource(`. "database/sql"`, `Open("sqlite", d)`), "dot import"},
		{"unparseable source", "x/bad.go", "package x\nfunc (", "cannot parse"},
		{"concatenated query", "x/q.go", plantedSource(std, `d := path + "?mode=ro"`), "built by hand"},
		{"file scheme concat", "x/f.go", plantedSource(std, `d := "file:" + path`), "built by hand"},
		{"sprintf query string", "x/sp.go", plantedSource(std, `d := fmt.Sprintf("%s?mode=ro", p)`), "built with Sprintf"},
		{"sprintf file scheme", "x/sf.go", plantedSource(std, `d := fmt.Sprintf("file:%s", p)`), "built with Sprintf"},
		{"driver in variable", "x/dv.go", plantedSource(std, `sql.Open(driverName, d)`), "outside dsn.go"},
		{"driver in constant", "x/dc.go", plantedSource(std, `sql.Open(sqliteDriver, d)`), "outside dsn.go"},
		{"sql.OpenDB connector", "x/od.go", plantedSource(std, `db := sql.OpenDB(conn)`), "driver reached without the helper"},
		{"aliased sql.OpenDB connector", "x/aod.go", plantedSource(`d "database/sql"`, `db := d.OpenDB(conn)`), "driver reached without the helper"},
		{"driver value Open", "x/do.go", plantedSource(std, `c, err := sqlite.Driver{}.Open(d)`), "driver reached without the helper"},
		{"aliased driver value Open", "x/ao.go", plantedSource(std, `c, err := drv.Driver{}.Open(d)`), "driver reached without the helper"},
		{"driver type used directly", "x/dt.go", plantedSource(std, `var drv sqlite.Driver`), "driver reached without the helper"},
		{"backtick sprintf query string", "x/bq.go", plantedSource(std, "d := fmt.Sprintf(`%s?mode=ro`, p)"), "built with Sprintf"},
		{"backtick sprintf file scheme", "x/bf.go", plantedSource(std, "d := fmt.Sprintf(`file:%s`, p)"), "built with Sprintf"},
		{"driver reached by a Driver call", "x/dcall.go", plantedSource(std, `c, err := s.Driver().Open(d)`), "driver reached without the helper"},
		{"sqlite driver type under an alias", "x/adrv.go", "package x\n\nimport d \"modernc.org/sqlite\"\n\nfunc f() {\n\tvar drv d.Driver\n\t_ = drv\n}\n", "driver reached without the helper"},
		{"sqlite driver Open under an alias", "x/aopen.go", "package x\n\nimport zz \"modernc.org/sqlite\"\n\nfunc f() {\n\tzz.Open(\"x\")\n}\n", "driver reached without the helper"},
		{"database/sql/driver Open under an alias", "x/drv.go", "package x\n\nimport dr \"database/sql/driver\"\n\nfunc f() {\n\tdr.Open(\"x\")\n}\n", "driver reached without the helper"},
		{"dot import of the sqlite driver", "x/ddot.go", "package x\n\nimport . \"modernc.org/sqlite\"\n\nfunc f() {}\n", "dot import"},
		{"attach database", "x/att.go", plantedSource(std, `db.Exec("ATTACH DATABASE ? AS other", p)`), "ATTACH in SQL text"},
		{"attach as, lower case", "x/att2.go", plantedSource(std, `db.Exec("attach ? as other", p)`), "ATTACH in SQL text"},
		{"attach in a raw string", "x/att3.go", plantedSource(std, "db.Exec(`ATTACH '/x/y.db' AS other`)"), "ATTACH in SQL text"},
		{"second vacuum into in another function of backup.go", "internal/store/sqlite/backup.go", "package sqlite\n\nfunc (s *Store) Other() {\n\ts.db.Exec(\"VACUUM INTO ?\", p)\n}\n", "VACUUM INTO outside"},
		{"vacuum into in a Backup method of another file", "internal/store/sqlite/other.go", "package sqlite\n\nfunc (s *Store) Backup() {\n\ts.db.Exec(\"VACUUM INTO ?\", p)\n}\n", "VACUUM INTO outside"},
		{"bound vacuum into in a plain function", "x/v2.go", plantedSource(std, `db.Exec("vacuum into ?", p)`), "VACUUM INTO outside"},
		{"same basename as the allowlisted tool in another directory", "other/tools/cleanup-test-schemas.go", "package main\n\nimport \"database/sql\"\n\nvar connectFn = sql.Open\n", "outside dsn.go"},
		{"vacuum literal", "x/v.go", plantedSource(std, `"VACUUM INTO '" + p + "'"`), "not a bound parameter"},
		{"vacuum sprintf", "x/s.go", plantedSource(std, `fmt.Sprintf("VACUUM INTO '%s'", p)`), "not a bound parameter"},
	}
	ok, err := dsnViolations(clean, 2)
	require.NoError(t, err)
	require.Empty(t, ok, "the clean fixture must pass")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{c.file: c.src}
			for k, v := range clean {
				files[k] = v
			}
			got, err := dsnViolations(files, 2)
			require.NoError(t, err)
			require.NotEmpty(t, got)
			assert.Contains(t, strings.Join(got, "\n"), c.want)
		})
	}
	t.Run("another package named sql is not database/sql", func(t *testing.T) {
		files := map[string]string{"x/other.go": plantedSource(`sql "example.com/other"`, `sql.Open("x")`)}
		for k, v := range clean {
			files[k] = v
		}
		got, err := dsnViolations(files, 2)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("prose and error values are not driver reaches", func(t *testing.T) {
		src := "package x\n\nimport (\n\t\"database/sql/driver\"\n\t\"fmt\"\n)\n\nfunc f(err error) error {\n\t_ = driver.ErrBadConn\n\treturn fmt.Errorf(\"drain attach queue: %w\", err)\n}\n"
		files := map[string]string{"x/prose.go": src}
		for k, v := range clean {
			files[k] = v
		}
		got, err := dsnViolations(files, 2)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("the one VACUUM INTO in Store.Backup is allowed", func(t *testing.T) {
		src := "package sqlite\n\nfunc (s *Store) Backup() {\n\ts.db.Exec(\"VACUUM INTO ?\", p)\n}\n"
		files := map[string]string{"internal/store/sqlite/backup.go": src}
		for k, v := range clean {
			files[k] = v
		}
		got, err := dsnViolations(files, 2)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("listed non-SQLite opener is skipped", func(t *testing.T) {
		files := map[string]string{"tools/cleanup-test-schemas.go": "package main\n\nimport \"database/sql\"\n\nvar connectFn = sql.Open\n"}
		for k, v := range clean {
			files[k] = v
		}
		got, err := dsnViolations(files, 2)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("listed non-SQLite opener that names sqlite is flagged", func(t *testing.T) {
		files := map[string]string{"tools/cleanup-test-schemas.go": "package main\n\nimport \"database/sql\"\n\nvar c = sql.Open // sqlite\n"}
		for k, v := range clean {
			files[k] = v
		}
		got, err := dsnViolations(files, 2)
		require.NoError(t, err)
		assert.Contains(t, strings.Join(got, "\n"), "names sqlite")
	})
	t.Run("an unlisted file with the same value use is flagged", func(t *testing.T) {
		files := map[string]string{"tools/other.go": "package main\n\nimport \"database/sql\"\n\nvar connectFn = sql.Open\n"}
		for k, v := range clean {
			files[k] = v
		}
		got, err := dsnViolations(files, 2)
		require.NoError(t, err)
		assert.Contains(t, strings.Join(got, "\n"), "outside dsn.go")
	})
	t.Run("missing helper open", func(t *testing.T) {
		got, err := dsnViolations(map[string]string{helper: "package sqlite", "a.go": "package a"}, 2)
		require.NoError(t, err)
		assert.Contains(t, strings.Join(got, "\n"), "found 0")
	})
	t.Run("scan that matched nothing errors", func(t *testing.T) {
		_, err := dsnViolations(map[string]string{}, dsnGuardMinFiles)
		require.Error(t, err)
	})
}

// TestSQLiteDSN_Build_EscapesPathAndAppendsParams pins the helper itself.
func TestSQLiteDSN_Build_EscapesPathAndAppendsParams(t *testing.T) {
	assert.Equal(t, "file:/t/a%3fb/c%23d/e%25f.db", sqliteDSN("/t/a?b/c#d/e%f.db"))
	assert.Equal(t, "file:/t/x.db?mode=ro&immutable=1", sqliteDSN("/t/x.db", "mode=ro", "immutable=1"))
	assert.Equal(t, "file:/t/it's q.db?_pragma=query_only(1)", sqliteDSN("/t/it's q.db", "_pragma=query_only(1)"))
}

// TestSQLiteDSN_ReadOnlyCallSites_PassModeRO pins the read-only intent at the
// call sites that cannot observe it by behavior (an existing file reads the
// same read-write or read-only): verify, recover and the inbox reader must
// each request mode=ro, and the inbox reader keeps its immutable fallback.
func TestSQLiteDSN_ReadOnlyCallSites_PassModeRO(t *testing.T) {
	cases := []struct{ file, want string }{
		{"backup.go", `openSQLite(path, "mode=ro")`},
		{"recover.go", `openSQLite(dbPath, "mode=ro")`},
		{"inbox_history.go", `countUnackedRO(ctx, dbPath, "mode=ro")`},
		{"inbox_history.go", `countUnackedRO(ctx, dbPath, "mode=ro", "immutable=1")`},
	}
	for _, c := range cases {
		b, err := os.ReadFile(c.file)
		require.NoError(t, err)
		assert.Contains(t, string(b), c.want, c.file)
	}
}
