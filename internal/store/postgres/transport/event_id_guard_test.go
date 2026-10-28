// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// eventIDName matches an identifier or field that holds a sync event id.
var eventIDName = regexp.MustCompile(`(?i)event_?ids?`)

// textSinks are the calls whose arguments reach an operator or a log.
var textSinks = map[string]bool{
	"fmt.Errorf": true, "fmt.Sprintf": true, "fmt.Sprint": true,
	"errors.New": true, "slog.String": true, "slog.Any": true,
}

// rawEventIDArgs returns a position for every argument of a text sink in
// src whose expression mentions an event id name without being exactly a
// format.EventID call (MTIX-187.4.4). The guard is positive: wrapping the
// id in string(), fmt.Sprint, strconv.Quote, a concatenation, a local
// identifier or strings.Join does not get it past.
func rawEventIDArgs(t *testing.T, filename, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	require.NoError(t, err)
	var bad []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !textSinks[calleeName(call)] {
			return true
		}
		for _, arg := range call.Args {
			if mentionsEventID(arg) && !isFormatEventID(arg) && !isLenCall(arg) {
				bad = append(bad, fset.Position(arg.Pos()).String())
			}
		}
		return true
	})
	return bad
}

func calleeName(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return pkg.Name + "." + sel.Sel.Name
}

func isFormatEventID(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	return ok && calleeName(call) == "format.EventID"
}

// isLenCall reports a len(...) call: a count of ids names no id.
func isLenCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "len"
}

func mentionsEventID(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			found = found || eventIDName.MatchString(x.Name)
		case *ast.SelectorExpr:
			found = found || eventIDName.MatchString(x.Sel.Name)
		}
		return true
	})
	return found
}

// TestTransportErrors_NeverTextRawEventIDs: no text sink in this package's
// production code receives an event id except through format.EventID.
func TestTransportErrors_NeverTextRawEventIDs(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(f)
		require.NoError(t, rerr)
		require.Empty(t, rawEventIDArgs(t, f, string(src)), "raw event id in a text sink of %s", f)
	}
}

// TestEventIDGuard_FlagsEveryBypass: the guard fails each way of getting a
// raw id into text, and passes the rendered form.
func TestEventIDGuard_FlagsEveryBypass(t *testing.T) {
	cases := []struct {
		name string
		call string
		bad  bool
	}{
		{"selector", `fmt.Errorf("x %s", e.EventID)`, true},
		{"string conversion", `fmt.Errorf("x %s", string(e.EventID))`, true},
		{"fmt.Sprint", `fmt.Errorf("x %s", fmt.Sprint(e.EventID))`, true},
		{"strconv.Quote", `fmt.Errorf("x %s", strconv.Quote(e.EventID))`, true},
		{"concatenation", `fmt.Errorf("x %s", "id="+e.EventID)`, true},
		{"local identifier", `fmt.Errorf("x %s", eventID)`, true},
		{"strings.Join", `fmt.Errorf("x %s", strings.Join(eventIDs, ", "))`, true},
		{"Sprintf sink", `fmt.Sprintf("x %s", e.HeldEventID)`, true},
		{"errors.New sink", `errors.New(e.EventID)`, true},
		{"slog sink", `slog.String("event_id", eventID)`, true},
		{"rendered", `fmt.Errorf("x %s", format.EventID(e.EventID))`, false},
		{"count of ids", `fmt.Errorf("x %d", len(eventIDs))`, false},
		{"unrelated", `fmt.Errorf("x %d", n)`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := rawEventIDArgs(t, "snippet.go", "package p\nvar _ = "+c.call+"\n")
			require.Equal(t, c.bad, len(bad) > 0, "%s", c.call)
		})
	}
}
