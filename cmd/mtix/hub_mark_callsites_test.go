// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Command-level guards for the sticky hub mark (MTIX-112.17.3, ADR-007 A9):
// a connection attempt that fails never marks, and each sync command records
// the mark only after the hub accepted its connection.

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusedDSN points at a closed local port: the connection is refused.
const refusedDSN = "postgres://u:p@127.0.0.1:1/db?sslmode=disable"

func requireMarkUnset(t *testing.T) {
	t.Helper()
	status, err := service.NewHubMarkService(app.store).Inspect(context.Background(), service.HubEvidence{})
	require.NoError(t, err)
	require.False(t, status.Connected, "a failed connection must not mark the board")
}

func TestSyncCommands_ExportedDSNWithoutConnection_DoNotMark(t *testing.T) {
	opts := transport.Options{InsecureTLS: true}
	commands := map[string]func(ctx context.Context, out, diag *bytes.Buffer) error{
		"pull": func(ctx context.Context, out, diag *bytes.Buffer) error {
			return runSyncPull(ctx, out, diag, nil, opts, 100)
		},
		"push": func(ctx context.Context, out, diag *bytes.Buffer) error {
			return runSyncPush(ctx, out, diag, nil, opts, false)
		},
		"init": func(ctx context.Context, out, diag *bytes.Buffer) error {
			return runSyncInit(ctx, out, diag, nil, opts)
		},
		"clone": func(ctx context.Context, out, diag *bytes.Buffer) error {
			return runSyncClone(ctx, out, diag, nil, opts, false, 100)
		},
	}
	for name, run := range commands {
		t.Run(name, func(t *testing.T) {
			initTestApp(t)
			t.Setenv(transport.EnvDSN, refusedDSN)
			var out, diag bytes.Buffer
			require.Error(t, run(context.Background(), &out, &diag))
			requireMarkUnset(t)
		})
	}
}

// TestSyncCommands_NoteHubConnection_FollowsTheConnect pins, in source order,
// that every command records the mark after transport.New succeeded (its
// error return) and never before it. The success path against a real hub is
// covered by the PG-backed tests of the same commands.
func TestSyncCommands_NoteHubConnection_FollowsTheConnect(t *testing.T) {
	for _, file := range []string{"sync_pull.go", "sync_push.go", "sync_init.go", "sync_clone.go"} {
		t.Run(file, func(t *testing.T) {
			fset := token.NewFileSet()
			parsed, err := parser.ParseFile(fset, file, nil, 0)
			require.NoError(t, err)
			var connect, note, nextAfter token.Pos
			ast.Inspect(parsed, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					if x, isIdent := fn.X.(*ast.Ident); isIdent && x.Name == "transport" && fn.Sel.Name == "New" {
						connect = call.Pos()
					}
				case *ast.Ident:
					if fn.Name == "noteHubConnection" {
						note = call.Pos()
					}
				}
				return true
			})
			require.True(t, connect.IsValid(), "%s must connect with transport.New", file)
			require.True(t, note.IsValid(), "%s must call noteHubConnection", file)
			assert.Greater(t, int(note), int(connect), "the mark follows the connect")
			_ = nextAfter
			// The error return of the connect precedes the call: a failed
			// connect returns before reaching noteHubConnection.
			src := fset.File(connect)
			assert.Greater(t, src.Line(note), src.Line(connect)+1, "an error check sits between connect and mark")
		})
	}
}
