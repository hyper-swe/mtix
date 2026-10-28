// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// TestMCPInboxAck_HostileAgent_SuccessTextIsEscaped drives the inbox ack call
// site end to end: the service accepts any agent string, so the escape is the
// only thing between a hostile value and the agent.
func TestMCPInboxAck_HostileAgent_SuccessTextIsEscaped(t *testing.T) {
	for _, f := range testutil.HostileText() {
		if f.Value == "" || len(f.Value) > 8192 || !validUTF8(f.Value) {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			env := newBehaviorTools(t)
			behaviorNode(t, env, "journal entry", "") // puts seq 1 inside the journal
			text, isErr := serverCallText(t, env, "mtix_inbox_ack", map[string]any{"agent": f.Value, "seq": 1})
			require.False(t, isErr, text)
			require.Equal(t, textInboxAcked(1, f.Value), text)
			requirePrintableToolText(t, text)
		})
	}
}

func validUTF8(s string) bool { return strings.ToValidUTF8(s, "") == s }

// TestSuccessResult_CallSites_NeverFormatRawValues pins the wiring of every
// plain-text success message. Dep add and remove, session end, heartbeat, claim
// and delete cannot be driven with a hostile value end to end, because their
// services reject it first, so a revert to a raw fmt.Sprintf would be silent.
// This guard parses the package and fails if any SuccessResult argument is
// built inline (fmt.Sprintf, concatenation) instead of by a text* formatter,
// which TestSuccessText_HostileValues_AreEscapedAtEverySite pins.
func TestSuccessResult_CallSites_NeverFormatRawValues(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "SuccessResult" {
				return true
			}
			checked++
			switch arg := call.Args[0].(type) {
			case *ast.BinaryExpr:
				t.Errorf("%s: SuccessResult argument is built by concatenation; use a text* formatter", fset.Position(arg.Pos()))
			case *ast.CallExpr:
				if sel, ok := arg.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "fmt" {
						t.Errorf("%s: SuccessResult argument uses fmt.%s; use a text* formatter", fset.Position(arg.Pos()), sel.Sel.Name)
					}
				}
			}
			return true
		})
	}
	require.Greater(t, checked, 20, "the guard must actually see the call sites")
	require.Equal(t, "a\\x1bb", format.SafeText("a\x1bb"), "guard relies on SafeText escaping controls")
}
