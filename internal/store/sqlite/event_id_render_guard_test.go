// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A type-aware guard for MTIX-187.4.3: no non-test file of this package may
// pass a sync event id to fmt.Errorf, fmt.Sprintf or errors.New except through
// format.EventID. Sites reachable only through a database or driver failure
// cannot be driven by a behavioural test; this guard covers them.

const guardModulePrefix = "github.com/hyper-swe/mtix/"

// guardImporter type-checks packages of this module from source, resolves the
// standard library through the source importer, and fakes every other
// third-party package as an empty one (selections on it stay invalid, which
// the guard tolerates because it only inspects event-id expressions).
type guardImporter struct {
	fset  *token.FileSet
	std   types.Importer
	root  string
	cache map[string]*types.Package
}

func newGuardImporter(fset *token.FileSet, moduleRoot string) *guardImporter {
	return &guardImporter{fset: fset, std: importer.ForCompiler(fset, "source", nil), root: moduleRoot,
		cache: map[string]*types.Package{}}
}

func (g *guardImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := g.cache[path]; ok {
		return pkg, nil
	}
	var pkg *types.Package
	var err error
	switch {
	case strings.HasPrefix(path, guardModulePrefix):
		pkg, err = g.checkLocal(path)
	case !strings.Contains(strings.SplitN(path, "/", 2)[0], "."):
		pkg, err = g.std.Import(path)
	default:
		pkg = types.NewPackage(path, filepath.Base(path))
		pkg.MarkComplete()
	}
	if err == nil {
		g.cache[path] = pkg
	}
	return pkg, err
}

func (g *guardImporter) checkLocal(path string) (*types.Package, error) {
	dir := filepath.Join(g.root, strings.TrimPrefix(path, guardModulePrefix))
	bp, err := build.Default.ImportDir(dir, 0)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, name := range bp.GoFiles {
		f, parseErr := parser.ParseFile(g.fset, filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			return nil, parseErr
		}
		files = append(files, f)
	}
	conf := types.Config{Importer: g, Error: func(error) {}, FakeImportC: true}
	pkg, _ := conf.Check(path, g.fset, files, nil)
	return pkg, nil
}

// eventIDSource reports whether e names a value that holds an event id: a
// struct field called EventID, a variable called eventID, or a variable the
// file binds to an event_id SQL parameter (tainted), resolved through the type
// information rather than by the spelling of the receiver.
func eventIDSource(info *types.Info, tainted map[types.Object]bool, e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return eventIDSource(info, tainted, x.X)
	case *ast.SelectorExpr:
		v, ok := info.Uses[x.Sel].(*types.Var)
		return x.Sel.Name == "EventID" && (!ok || v.IsField())
	case *ast.Ident:
		obj := info.Uses[x]
		v, ok := obj.(*types.Var)
		return ok && !v.IsField() && (strings.EqualFold(v.Name(), "eventID") || tainted[obj])
	}
	return false
}

// isFormatEventIDCall reports whether call's callee resolves, through the
// type information, to format.EventID of this module. A wrapper with another
// name, strconv.Quote and a conversion are all different callees.
func isFormatEventIDCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == guardModulePrefix+"internal/format" && fn.Name() == "EventID"
}

// eventIDPlaceholder matches the text before a ? that binds the event_id
// column: "event_id = ?" or "event_id=?".
var eventIDPlaceholder = regexp.MustCompile(`\bevent_id\s*=\s*$`)

// eventIDPlaceholders returns, for each ? of query in order, whether it binds
// the event_id column.
func eventIDPlaceholders(query string) []bool {
	var out []bool
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out = append(out, eventIDPlaceholder.MatchString(query[:i]))
		}
	}
	return out
}

// taintedEventIDVars finds variables passed as the bound argument of an
// "event_id = ?" placeholder of a database call whose query text is constant:
// they hold event ids whatever they are called (the loop variable of
// SetPushHoldReasons).
func taintedEventIDVars(info *types.Info, files []*ast.File) map[types.Object]bool {
	tainted := map[types.Object]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for i, arg := range call.Args {
				tv, has := info.Types[arg]
				if !has || tv.Value == nil || tv.Value.Kind() != constant.String {
					continue
				}
				for k, binds := range eventIDPlaceholders(constant.StringVal(tv.Value)) {
					at := i + 1 + k
					if !binds || at >= len(call.Args) {
						continue
					}
					if id, isIdent := call.Args[at].(*ast.Ident); isIdent {
						if v, isVar := info.Uses[id].(*types.Var); isVar && !v.IsField() {
							tainted[v] = true
						}
					}
				}
			}
			return true
		})
	}
	return tainted
}

// containsRawEventID reports whether e holds an event id that is not wrapped
// in a call to format.EventID: the wrapped subtree is not inspected, every
// other call (strconv.Quote, a conversion, a local wrapper) is looked through.
func containsRawEventID(info *types.Info, tainted map[types.Object]bool, e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && isFormatEventIDCall(info, call) {
			return false
		}
		if x, ok := n.(ast.Expr); ok && eventIDSource(info, tainted, x) {
			found = true
			return false
		}
		return true
	})
	return found
}

// isFormatterCall reports whether call is fmt.Errorf, fmt.Sprintf or errors.New.
func isFormatterCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	pn, ok := info.Uses[id].(*types.PkgName)
	if !ok {
		return false
	}
	switch pn.Imported().Path() + "." + sel.Sel.Name {
	case "fmt.Errorf", "fmt.Sprintf", "errors.New":
		return true
	}
	return false
}

// rawEventIDArgs scans files and returns one position per raw event-id
// argument of a formatter call, and how many formatter calls it saw.
func rawEventIDArgs(fset *token.FileSet, files []*ast.File, info *types.Info) (raw []string, calls int) {
	tainted := taintedEventIDVars(info, files)
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isFormatterCall(info, call) {
				return true
			}
			calls++
			for _, arg := range call.Args {
				if containsRawEventID(info, tainted, arg) {
					raw = append(raw, fset.Position(arg.Pos()).String())
				}
			}
			return true
		})
	}
	sort.Strings(raw)
	return raw, calls
}

func checkGuardFiles(t *testing.T, fset *token.FileSet, files []*ast.File, imp types.Importer, pkgPath string) ([]string, int) {
	t.Helper()
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{}}
	conf := types.Config{Importer: imp, Error: func(error) {}, FakeImportC: true}
	_, _ = conf.Check(pkgPath, fset, files, info)
	return rawEventIDArgs(fset, files, info)
}

// Every fmt.Errorf, fmt.Sprintf and errors.New of the package's non-test files
// renders an event id through format.EventID (MTIX-187.4.3).
func TestEventIDRenderGuard_NonTestFiles_NoRawEventID(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "go.mod"))
	require.NoError(t, err, "module root must be found from the package directory")

	bp, err := build.Default.ImportDir(".", 0)
	require.NoError(t, err)
	require.NotEmpty(t, bp.GoFiles, "the guard must scan at least one file")
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range bp.GoFiles {
		f, parseErr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, parseErr)
		files = append(files, f)
	}
	raw, calls := checkGuardFiles(t, fset, files, newGuardImporter(fset, root), guardModulePrefix+"internal/store/sqlite")
	require.Greater(t, calls, 100, "the guard must find the package's formatter calls, or it checks nothing")
	require.Empty(t, raw, "event ids must be wrapped in format.EventID in these formatter calls")
}

// The guard itself: it flags a raw field, a raw variable, a named string
// type, a parenthesised id, an id rendered by strconv.Quote, by a percent-q
// verb, by a string conversion or by a local wrapper, and a differently named
// variable bound to an event_id SQL parameter; it passes only format.EventID
// and unrelated strings, and a scan of nothing finds nothing to trust.
func TestEventIDRenderGuard_SelfTest_FlagsRawAndPassesWrapped(t *testing.T) {
	src := `package sample

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/hyper-swe/mtix/internal/format"
)

type rowID string
type event struct {
	EventID string
	Named   rowID
	Other   string
}
type named struct{ EventID rowID }

func wrap(s string) string { return s }

func cases(e event, n named, eventID string, other string) []error {
	return []error{
		fmt.Errorf("raw field %s: %w", e.EventID, errors.New("x")), // flagged
		fmt.Errorf("raw var %s", eventID),                           // flagged
		fmt.Errorf("raw paren %s", (e.EventID)),                     // flagged
		fmt.Errorf("raw named %s", n.EventID),                       // flagged
		errors.New(eventID),                                         // flagged
		fmt.Errorf("quote %s", strconv.Quote(e.EventID)),            // flagged
		fmt.Errorf("verb %q", eventID),                              // flagged
		fmt.Errorf("conv %s", string([]byte(e.EventID))),            // flagged
		fmt.Errorf("local wrapper %s", wrap(e.EventID)),             // flagged
		fmt.Errorf("wrapped %s", format.EventID(e.EventID)),
		fmt.Errorf("wrapped var %s", format.EventID(eventID)),
		fmt.Errorf("unrelated %s", e.Other),
		fmt.Errorf("unrelated var %s", other),
	}
}

func tainted(ctx context.Context, tx *sql.Tx, ids []string, nodeID string) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "UPDATE t SET r = ? WHERE event_id = ?", "r", id); err != nil {
			return fmt.Errorf("flagged bound id %s: %w", id, err) // flagged
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE t SET r = ? WHERE node_id = ?", "r", nodeID); err != nil {
		return fmt.Errorf("unrelated node %s: %w", nodeID, err)
	}
	return nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "sample.go", src, 0)
	require.NoError(t, err)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	raw, calls := checkGuardFiles(t, fset, []*ast.File{f}, newGuardImporter(fset, root), "sample")
	require.Equal(t, 16, calls)
	var want, got []string
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "// flagged") {
			want = append(want, "sample.go:"+strconv.Itoa(i+1))
		}
	}
	for _, pos := range raw {
		got = append(got, strings.Join(strings.SplitN(pos, ":", 3)[:2], ":"))
	}
	sort.Strings(want)
	require.Equal(t, want, got, "exactly the lines marked flagged are reported; format.EventID passes")
	require.Len(t, want, 10)

	noFiles, noCalls := checkGuardFiles(t, fset, nil, importer.ForCompiler(fset, "source", nil), "empty")
	require.Empty(t, noFiles)
	require.Zero(t, noCalls, "an empty scan reports zero calls, which the main guard rejects")
}
