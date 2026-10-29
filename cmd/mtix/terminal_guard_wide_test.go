// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// Positive controls: every sink and field in the widened guard must be caught
// in wide mode, so deleting one from the lists fails here.
func TestTerminalGuard_WideSinksAndFields_AreFlagged(t *testing.T) {
	sinks := []string{
		`cmd.Print(%s)`, `cmd.Println(%s)`, `cmd.Printf("%%s", %s)`, `cmd.PrintErr(%s)`, `cmd.PrintErrln(%s)`, `cmd.PrintErrf("%%s", %s)`,
		`log.Printf("%%s", %s)`, `log.Println(%s)`, `log.Fatalf("%%s", %s)`, `log.Panicf("%%s", %s)`,
		`slog.Info("m", %s)`, `slog.Warn("m", %s)`, `slog.Error("m", %s)`, `slog.Debug("m", %s)`,
		`logger.Info("m", %s)`, `logger.ErrorContext(ctx, "m", %s)`,
		`io.WriteString(w, %s)`, `w.Write([]byte(%s))`, `w.WriteString(%s)`, `os.Stdout.WriteString(%s)`, `os.Stderr.Write([]byte(%s))`,
		`fmt.Fprintf(w, "%%s", %s)`,
	}
	fields := []string{"n.EventID", "n.Title", "n.NodeID", "n.Reason", "n.Assignee", "n.UID", "n.HeldUID", "n.IncomingUID", "n.HubUID", "n.LocalUID"}
	for _, sink := range sinks {
		for _, field := range fields {
			call := fmt.Sprintf(sink, field)
			require.NotEmpty(t, terminalRawCalls(terminalWideSnippet(t, call)), call)
		}
	}
}

func TestTerminalGuard_WideTaintShapes_AreFlagged(t *testing.T) {
	for _, body := range []string{
		`for _, t := range []string{title} { fmt.Println(t) }`,
		`for _, t := range []string{n.Title} { fmt.Println(t) }`,
		`v, err := get(n.Title); _ = err; fmt.Println(v)`,
		`a, b, _ := strings.Cut(n.Reason, ":"); fmt.Println(a, b)`,
	} {
		require.NotEmpty(t, terminalRawCalls(terminalWideSnippet(t, body)), body)
	}
	for _, body := range []string{
		`v, err := get(n.Title); fmt.Println(err); _ = v`,
		`reason := "x"; fmt.Println(reason)`,
		`uid := 1; fmt.Println(uid)`,
		`fmt.Println(oneLine(n.NodeID), displayHoldReason(n.Reason), format.SafeLine(n.Title))`,
	} {
		require.Empty(t, terminalRawCalls(terminalWideSnippet(t, body)), body)
	}
}

// internal/ is scanned in narrow mode: error text there is out of scope.
func TestTerminalGuard_NarrowMode_IgnoresExtendedSinksAndFields(t *testing.T) {
	file := terminalWideSnippet(t, `cmd.Println(n.NodeID); return fmt.Errorf("x %s", n.Reason)`)
	require.Empty(t, terminalRawCallsMode(file, false))
	require.NotEmpty(t, terminalRawCallsMode(terminalWideSnippet(t, `fmt.Println(n.Title)`), false))
}

// The narrow scan (internal/) must not follow range or tuple aliases.
func TestTerminalGuard_NarrowMode_SkipsRangeAndTupleTaint(t *testing.T) {
	for _, body := range []string{
		`for _, t := range []string{n.Title} { fmt.Println(t) }`,
		`v, err := get(n.Title); _ = err; fmt.Println(v)`,
	} {
		require.Empty(t, terminalRawCallsMode(terminalWideSnippet(t, body), false), body)
		require.NotEmpty(t, terminalRawCallsMode(terminalWideSnippet(t, body), true), body)
	}
}

// Error-named tuple results are exempt in wide mode.
func TestTerminalGuard_ErrorNamedTupleResults_AreNotTainted(t *testing.T) {
	for _, name := range []string{"err", "applyErr", "parseErr", "ok"} {
		body := "v, " + name + " := get(n.Title); _ = v; fmt.Println(" + name + ")"
		require.Empty(t, terminalRawCalls(terminalWideSnippet(t, body)), body)
	}
}

// An unlisted format.* helper is not safe: only the pinned list is.
func TestTerminalGuard_SafeHelperList_IsPinned(t *testing.T) {
	require.NotEmpty(t, terminalRawCalls(terminalSnippet(t, `output.Printf("%s", f.Other(n.Title))`)))
	require.Equal(t, map[string]bool{"EventID": true, "SafeLine": true, "StripControlChars": true}, terminalSafeHelpers)
	require.Equal(t, map[string]bool{"oneLine": true, "displayHoldReason": true}, terminalLocalSafe)
}

func TestTerminalGuard_StaleEntries_AreReported(t *testing.T) {
	allowed := map[string]bool{"a.go:f:Printf:1": true, "b.go:g:Printf:2": true}
	require.Empty(t, terminalStaleEntries(allowed, map[string]bool{"a.go:f:Printf:1": true, "b.go:g:Printf:2": true}))
	stale := terminalStaleEntries(allowed, map[string]bool{"a.go:f:Printf:1": true})
	require.Equal(t, []string{"stale terminal allowlist entry: b.go:g:Printf:2"}, stale)
}

func terminalWideSnippet(t *testing.T, body string) *ast.File {
	t.Helper()
	src := fmt.Sprintf(`package p
 import "fmt"
 import "io"
 import "log"
 import "log/slog"
 import "os"
 import "strings"
 import "github.com/hyper-swe/mtix/internal/format"
 func render() { %s }`, body)
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
	require.NoError(t, err)
	return file
}
