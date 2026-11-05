// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
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
	sqliteOpenRe = regexp.MustCompile(`sql\.Open\(`)
	// otherOpenRe matches the other ways to reach the driver without the
	// helper: sql.OpenDB with a connector, and a driver value's own Open.
	otherOpenRe    = regexp.MustCompile(`sql\.OpenDB\(|\bsqlite3?\.(Driver|Open)\b|\bDriver\{\}\.Open\(`)
	dsnLiteralRe   = regexp.MustCompile(`"file:"\s*\+|"\?(mode|_pragma|_txlock|immutable|cache)=|\+\s*"\?`)
	sprintfDSNRe   = regexp.MustCompile(`Sprintf\(\s*["` + "`" + `][^"` + "`" + `]*(file:|\?(mode|_pragma|_txlock|immutable|cache)=)`)
	vacuumLiteralR = regexp.MustCompile(`VACUUM INTO\s*'|VACUUM INTO[^"` + "`" + `\n]*%`)
)

// dsnGuardMinFiles is the least number of non-test Go files a scan must see.
// Fewer means the walk matched the wrong tree, and a guard that scanned
// nothing would pass for the wrong reason.
const dsnGuardMinFiles = 100

// dsnViolations scans non-test Go sources (path -> text) and returns one
// line per breach of the single-builder rule (MTIX-112.2.4): the only
// sql.Open (whatever its driver argument, so a driver name held in a variable
// or constant is caught too) is in dsn.go; no DSN or query string is spelled out
// anywhere else; VACUUM INTO never takes its target as a literal. It errors
// when it was given too few files to mean anything.
func dsnViolations(files map[string]string, minFiles int) ([]string, error) {
	if len(files) < minFiles {
		return nil, fmt.Errorf("dsn guard scanned %d files, need at least %d: the walk matched nothing", len(files), minFiles)
	}
	var out []string
	opens := 0
	for path, text := range files {
		src := dropCommentLines(text)
		isHelper := filepath.Base(path) == "dsn.go" && strings.Contains(filepath.ToSlash(path), "internal/store/sqlite/")
		if n := len(sqliteOpenRe.FindAllString(src, -1)); n > 0 {
			opens += n
			if !isHelper {
				out = append(out, path+": sql.Open outside dsn.go")
			}
		}
		if !isHelper && otherOpenRe.MatchString(src) {
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
	}
	if opens != 1 {
		out = append(out, fmt.Sprintf("expected exactly one sql.Open(\"sqlite\") in dsn.go, found %d", opens))
	}
	sort.Strings(out)
	return out, nil
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

// TestDSNViolations_PlantedBreaches_AreCaught proves the scanner fails on
// each planted violation, and on a scan that saw too little to judge.
func TestDSNViolations_PlantedBreaches_AreCaught(t *testing.T) {
	helper := "internal/store/sqlite/dsn.go"
	clean := map[string]string{helper: `db, err := sql.Open("sqlite", sqliteDSN(p))`, "a/b.go": "package b"}
	cases := []struct{ name, file, src, want string }{
		{"second open", "x/open.go", `sql.Open("sqlite", d)`, "outside dsn.go"},
		{"concatenated query", "x/q.go", `d := path + "?mode=ro"`, "built by hand"},
		{"file scheme concat", "x/f.go", `d := "file:" + path`, "built by hand"},
		{"sprintf query string", "x/sp.go", `d := fmt.Sprintf("%s?mode=ro", p)`, "built with Sprintf"},
		{"sprintf file scheme", "x/sf.go", `d := fmt.Sprintf("file:%s", p)`, "built with Sprintf"},
		{"driver in variable", "x/dv.go", `sql.Open(driverName, d)`, "outside dsn.go"},
		{"driver in constant", "x/dc.go", `sql.Open(sqliteDriver, d)`, "outside dsn.go"},
		{"sql.OpenDB connector", "x/od.go", `db := sql.OpenDB(conn)`, "driver reached without the helper"},
		{"driver value Open", "x/do.go", `c, err := sqlite.Driver{}.Open(d)`, "driver reached without the helper"},
		{"aliased driver value Open", "x/ao.go", `c, err := drv.Driver{}.Open(d)`, "driver reached without the helper"},
		{"driver type used directly", "x/dt.go", `var drv sqlite.Driver`, "driver reached without the helper"},
		{"backtick sprintf query string", "x/bq.go", "d := fmt.Sprintf(`%s?mode=ro`, p)", "built with Sprintf"},
		{"backtick sprintf file scheme", "x/bf.go", "d := fmt.Sprintf(`file:%s`, p)", "built with Sprintf"},
		{"vacuum literal", "x/v.go", `"VACUUM INTO '" + p + "'"`, "not a bound parameter"},
		{"vacuum sprintf", "x/s.go", `fmt.Sprintf("VACUUM INTO '%s'", p)`, "not a bound parameter"},
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
