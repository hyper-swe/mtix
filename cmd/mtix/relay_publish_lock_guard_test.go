// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRelayPublishLock_EveryStackIsBuiltWithTheLockDir is the FR-21
// §6.2a item 7 source scan for the CLI: every tick.StackConfig literal
// outside tests names LockDir, so no entry point (daemon tick, sync relay
// tick, doctor, reset-peer) can publish without the cross-process lock.
func TestRelayPublishLock_EveryStackIsBuiltWithTheLockDir(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	literals := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(name)
		require.NoError(t, rerr)
		f, perr := parser.ParseFile(token.NewFileSet(), name, src, 0)
		require.NoError(t, perr)
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := cl.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "StackConfig" {
				return true
			}
			literals++
			has := false
			for _, e := range cl.Elts {
				if kv, isKV := e.(*ast.KeyValueExpr); isKV && kv.Key.(*ast.Ident).Name == "LockDir" {
					has = true
				}
			}
			require.True(t, has, "%s builds a relay stack without LockDir", name)
			return true
		})
	}
	require.NotZero(t, literals, "the scan found no relay stack construction")
}
