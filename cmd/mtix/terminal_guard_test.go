// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	gofmt "go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerminalGuard_AST prevents direct and locally aliased hostile-text
// fields (EventID, Title, NodeID, Reason, Assignee and the UID family) from
// reaching terminal sinks (MTIX-187.7.5).
//
// Sinks: fmt print/format/Errorf; cobra cmd.Print*/PrintErr*; log Print*,
// Fatal*, Panic*; log/slog functions and logger methods; io.WriteString; and
// Write/WriteString on any receiver (os.Stdout, os.Stderr, an io.Writer).
//
// Limits: this is a syntax guard, not whole-program taint analysis. It follows
// local assignments, var declarations, range variables and tuple results
// within one function. It does not follow values returned by unknown single-
// result helpers, struct fields assigned from raw text, other functions'
// parameters, or text built in a strings.Builder and printed later. Those
// paths need behavioral coverage in TestTerminalBinary_*.
func TestTerminalGuard_AST(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := terminalAllowlist(t)
	used := map[string]bool{}
	for _, dir := range []string{"cmd/mtix", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			for _, violation := range terminalRawCallsMode(file, dir == "cmd/mtix") {
				key := filepath.ToSlash(rel) + ":" + violation
				if allowed[key] {
					used[key] = true
				} else {
					t.Errorf("raw terminal argument: %s", key)
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	for _, problem := range terminalStaleEntries(allowed, used) {
		t.Error(problem)
	}
}

// terminalStaleEntries reports allowlist entries no violation used, so a
// changed or removed call cannot leave a silent exemption behind.
func terminalStaleEntries(allowed, used map[string]bool) []string {
	var stale []string
	for key := range allowed {
		if !used[key] {
			stale = append(stale, "stale terminal allowlist entry: "+key)
		}
	}
	sort.Strings(stale)
	return stale
}

func terminalAllowlist(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("terminal_guard_allowlist.txt")
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	allowed := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, reason, ok := strings.Cut(line, " | ")
		require.True(t, ok && reason != "", "allowlist needs a reason: %s", line)
		require.False(t, allowed[key], "duplicate allowlist: %s", key)
		allowed[key] = true
	}
	require.NoError(t, scanner.Err())
	return allowed
}

func terminalRawCalls(file *ast.File) []string { return terminalRawCallsMode(file, true) }

// terminalRawCallsMode scans one file; wide selects the cmd/mtix mode.
func terminalRawCallsMode(file *ast.File, wide bool) []string {
	imports := terminalImports(file)
	imports.wide = wide
	var violations []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for _, call := range terminalFunctionCalls(fn, imports) {
			violations = append(violations, fn.Name.Name+":"+terminalCallKey(call))
		}
	}
	return violations
}

// terminalCtx carries a file's import names and the scan mode. wide enables
// the cobra/log/slog/Write sinks and the extended selector fields; it applies
// to cmd/mtix terminal writers. internal/ is scanned in the narrow mode
// (fmt sinks; EventID/Title only), because its error text is out of scope.
type terminalCtx struct {
	pkgs map[string]string
	wide bool
}

func terminalImports(file *ast.File) terminalCtx {
	imports := map[string]string{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imports[name] = path
	}
	return terminalCtx{pkgs: imports, wide: true}
}

func terminalFunctionCalls(fn *ast.FuncDecl, imports terminalCtx) []*ast.CallExpr {
	tainted := terminalTaint(fn.Body, imports)
	var calls []*ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !terminalSink(call.Fun, imports) {
			return true
		}
		for _, arg := range call.Args {
			if terminalRaw(arg, imports, tainted) {
				calls = append(calls, call)
				break
			}
		}
		return true
	})
	return calls
}

// A conservative fixed point catches local aliases through chains/branches.
// See TestTerminalGuard_AST for the guard's limits.
func terminalTaint(body *ast.BlockStmt, imports terminalCtx) map[string]bool {
	tainted := map[string]bool{}
	for changed := true; changed; {
		changed = false
		ast.Inspect(body, func(n ast.Node) bool {
			lhs, rhs := terminalBindings(n)
			tuple := imports.wide && len(rhs) == 1 && len(lhs) > 1
			if !imports.wide {
				if _, isRange := n.(*ast.RangeStmt); isRange {
					return true
				}
			}
			if tuple {
				// v, err := f(raw) taints v; err and blanks are excluded so
				// ordinary error wrapping is not flagged.
				rhs = terminalRepeat(rhs[0], len(lhs))
				lhs, rhs = terminalDropErr(lhs, rhs)
			}
			if len(lhs) != len(rhs) {
				return true
			}
			for i, name := range lhs {
				if tainted[name] {
					continue
				}
				if (tuple && terminalRaw(rhs[i], imports, tainted)) || (!tuple && terminalAliasRaw(rhs[i], imports, tainted)) {
					tainted[name], changed = true, true
				}
			}
			return true
		})
	}
	return tainted
}

func terminalBindings(n ast.Node) ([]string, []ast.Expr) {
	var names []string
	switch x := n.(type) {
	case *ast.AssignStmt:
		for _, e := range x.Lhs {
			id, ok := e.(*ast.Ident)
			if !ok {
				return nil, nil
			}
			names = append(names, id.Name)
		}
		return names, x.Rhs
	case *ast.ValueSpec:
		for _, id := range x.Names {
			names = append(names, id.Name)
		}
		return names, x.Values
	case *ast.RangeStmt:
		// Range key and value alias elements of the ranged expression.
		for _, e := range []ast.Expr{x.Key, x.Value} {
			if id, ok := e.(*ast.Ident); ok {
				names = append(names, id.Name)
			}
		}
		return names, terminalRepeat(x.X, len(names))
	}
	return nil, nil
}

func terminalRepeat(e ast.Expr, n int) []ast.Expr {
	out := make([]ast.Expr, n)
	for i := range out {
		out[i] = e
	}
	return out
}

// terminalDropErr removes blank, ok and error-named results (err, applyErr,
// ...) from a tuple binding; errors are wrapped, not printed as stored text.
func terminalDropErr(lhs []string, rhs []ast.Expr) ([]string, []ast.Expr) {
	var names []string
	var exprs []ast.Expr
	for i, name := range lhs {
		if name == "_" || name == "ok" || strings.HasSuffix(strings.ToLower(name), "err") {
			continue
		}
		names = append(names, name)
		exprs = append(exprs, rhs[i])
	}
	return names, exprs
}

// Calls returning errors, rows, lengths, etc. do not return the input text.
func terminalAliasRaw(e ast.Expr, imports terminalCtx, tainted map[string]bool) bool {
	if call, ok := e.(*ast.CallExpr); ok && !terminalSink(call.Fun, imports) {
		sel, selected := call.Fun.(*ast.SelectorExpr)
		if !selected {
			return false
		}
		id, named := sel.X.(*ast.Ident)
		if !named || imports.pkgs[id.Name] != "strings" {
			return false
		}
	}
	return terminalRaw(e, imports, tainted)
}

// The fingerprint permits only this exact call, never another sink in the
// same function. Changes require a fresh review of the exception's reason.
func terminalCallKey(call *ast.CallExpr) string {
	var source bytes.Buffer
	if err := gofmt.Node(&source, token.NewFileSet(), call); err != nil {
		return "invalid"
	}
	sum := sha256.Sum256(source.Bytes())
	return fmt.Sprintf("%s:%x", terminalCallName(call.Fun), sum[:8])
}

func terminalCallName(e ast.Expr) string {
	if s, ok := e.(*ast.SelectorExpr); ok {
		return s.Sel.Name
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

var (
	terminalCobraSinks = map[string]bool{"Print": true, "Println": true, "Printf": true, "PrintErr": true, "PrintErrln": true, "PrintErrf": true}
	terminalSlogSinks  = map[string]bool{"Debug": true, "Info": true, "Warn": true, "Error": true, "Log": true, "DebugContext": true, "InfoContext": true, "WarnContext": true, "ErrorContext": true, "LogAttrs": true}
	// Hostile-text fields; matched as selectors and, case-insensitively, as
	// identifiers (local variables and parameters named after the field).
	terminalFields = []string{"EventID", "Title", "NodeID", "Reason", "Assignee", "UID", "HeldUID", "IncomingUID", "HubUID", "LocalUID"}
	// format helpers that render hostile text safely for a terminal.
	// cmd/mtix helpers that strip controls (oneLine) or render event ids and
	// held reasons safely (displayHoldReason); see sync_event_id_reason.go.
	terminalLocalSafe   = map[string]bool{"oneLine": true, "displayHoldReason": true}
	terminalSafeHelpers = map[string]bool{"EventID": true, "SafeLine": true, "StripControlChars": true}
)

// terminalSink reports whether e is a terminal-bound call target. Package
// functions are matched by import path; method calls by name on any receiver.
func terminalSink(e ast.Expr, imports terminalCtx) bool {
	name := terminalCallName(e)
	s, ok := e.(*ast.SelectorExpr)
	if !ok {
		return imports.pkgs["."] == "fmt" && terminalFmtSink(name)
	}
	if id, named := s.X.(*ast.Ident); named && imports.pkgs[id.Name] != "" {
		return terminalPackageSink(imports.pkgs[id.Name], name, imports.wide)
	}
	// Method call: cobra cmd.Print*, io.Writer/os.File Write*, logger methods.
	return imports.wide && (terminalCobraSinks[name] || terminalSlogSinks[name] || name == "Write" || name == "WriteString")
}

// terminalPackageSink matches package-level functions by import path; only fmt
// applies outside wide mode.
func terminalPackageSink(path, name string, wide bool) bool {
	if path == "fmt" {
		return terminalFmtSink(name)
	}
	if !wide {
		return false
	}
	switch path {
	case "log":
		return strings.HasPrefix(name, "Print") || strings.HasPrefix(name, "Fatal") || strings.HasPrefix(name, "Panic")
	case "log/slog":
		return terminalSlogSinks[name]
	case "io":
		return name == "WriteString"
	}
	return false
}

func terminalFmtSink(name string) bool {
	return strings.Contains(name, "Print") || strings.Contains(name, "print") || name == "Errorf"
}

func terminalRaw(e ast.Expr, imports terminalCtx, tainted map[string]bool) bool {
	raw := false
	ast.Inspect(e, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && terminalSafeCall(call, imports) {
			return false
		}
		switch x := n.(type) {
		case *ast.SelectorExpr:
			raw = raw || terminalSelector(x.Sel.Name, imports.wide)
		case *ast.Ident:
			raw = raw || tainted[x.Name] || strings.EqualFold(x.Name, "eventID") || x.Name == "title"
		}
		return !raw
	})
	return raw
}

func terminalSafeCall(call *ast.CallExpr, imports terminalCtx) bool {
	if id, local := call.Fun.(*ast.Ident); local {
		// Package-local sanitizers defined in cmd/mtix.
		return imports.wide && terminalLocalSafe[id.Name]
	}
	s, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	if !ok || imports.pkgs[id.Name] != "github.com/hyper-swe/mtix/internal/format" {
		return false
	}
	return terminalSafeHelpers[s.Sel.Name]
}

// terminalSelector matches x.Field. EventID and Title are always hostile
// fields; the extended set applies in wide mode and, by ruling, only as
// selectors (a bare local named reason or uid is not flagged).
func terminalSelector(name string, wide bool) bool {
	if name == "EventID" || name == "Title" {
		return true
	}
	if !wide {
		return false
	}
	for _, f := range terminalFields[2:] {
		if name == f {
			return true
		}
	}
	return false
}

func TestTerminalGuard_ASTMatcher(t *testing.T) {
	for _, expr := range []string{"n.Title", "e.EventID", "title", "eventID", "strings.TrimSpace(n.Title)", "output.Sprintf(\"%s\", e.EventID)", "f.SafeLine(n.Title)+e.EventID"} {
		t.Run(expr, func(t *testing.T) {
			for _, bind := range []string{"alias := ", "var alias = "} {
				file := terminalSnippet(t, bind+expr+"; next := alias; output.Printf(\"%s\", next)")
				require.NotEmpty(t, terminalRawCalls(file))
			}
		})
	}
	for _, expr := range []string{"f.SafeLine(n.Title)", "f.EventID(e.EventID)", "f.StripControlChars(n.Title)", "\"Title\""} {
		require.Empty(t, terminalRawCalls(terminalSnippet(t, "output.Printf(\"%s\", "+expr+")")))
	}
	for _, sink := range []string{"Print", "Println", "Printf", "Fprint", "Fprintln", "Fprintf", "Sprint", "Sprintln", "Sprintf", "Errorf"} {
		require.NotEmpty(t, terminalRawCalls(terminalSnippet(t, "output."+sink+"(e.EventID)")), sink)
	}
}

func terminalSnippet(t *testing.T, body string) *ast.File {
	t.Helper()
	src := fmt.Sprintf(`package p
 import output "fmt"
 import "strings"
 import f "github.com/hyper-swe/mtix/internal/format"
 func render() { %s }`, body)
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
	require.NoError(t, err)
	return file
}
