// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// These tests pin SQL Rule 1 compliance and the pre-LWW refusal boundary.
func literalTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "literal.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func TestDetectLWWOutcome_UnknownFieldBeforeSQL(t *testing.T) {
	// A nil transaction proves there cannot be a lookup: even issuing SQL
	// and then overriding its error would panic before returning InvalidInput.
	for _, uid := range []string{"", "0193fa00-0000-7000-8000-000000000001"} {
		for _, field := range []string{"", "unknown", "title; DROP TABLE nodes", "TITLE"} {
			t.Run(uid+"/"+field, func(t *testing.T) {
				e := makeApplyEvent(t, model.OpUpdateField, "TEST-1", "alice", 1,
					model.UpdateFieldPayload{FieldName: field})
				e.UID = uid
				var err error
				require.NotPanics(t, func() {
					_, err = detectLWWOutcome(context.Background(), nil, e)
				})
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.NotErrorIs(t, err, sql.ErrTxDone)
			})
		}
	}
}

func TestDetectLWWOutcome_AllowedFieldsRetainLookup(t *testing.T) {
	s := literalTestStore(t)
	for _, uid := range []string{"", "0193fa00-0000-7000-8000-000000000001"} {
		for _, field := range []string{"title", "description", "prompt", "acceptance", "status", "priority", "labels", "assignee", "agent_state", "issue_type"} {
			t.Run(uid+"/"+field, func(t *testing.T) {
				tx, err := s.writeDB.BeginTx(context.Background(), nil)
				require.NoError(t, err)
				defer func() { require.NoError(t, tx.Rollback()) }()
				e := makeApplyEvent(t, model.OpUpdateField, "TEST-1", "alice", 1,
					model.UpdateFieldPayload{FieldName: field})
				e.UID = uid
				outcome, err := detectLWWOutcome(context.Background(), tx, e)
				require.NoError(t, err)
				require.False(t, outcome.HasPrior)
				require.Equal(t, field, outcome.FieldName)
			})
		}
	}
}

func TestClearAllTables_LiteralStatementsInFKOrder(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "import.go", nil, 0)
	require.NoError(t, err)
	var statements []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "clearAllTables" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ExecContext" {
				return true
			}
			require.Len(t, call.Args, 2)
			lit, ok := call.Args[1].(*ast.BasicLit)
			require.True(t, ok, "executed DELETE must be a literal")
			if ok {
				text, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				statements = append(statements, text)
			}
			return true
		})
	}
	require.Equal(t, []string{"DELETE FROM dependencies", "DELETE FROM sessions", "DELETE FROM agents", "DELETE FROM nodes", "DELETE FROM sequences"}, statements)
}

func TestClearAllTables_DeletionErrorsPreserveContext(t *testing.T) {
	tests := []struct{ table, trigger string }{
		{"dependencies", `CREATE TRIGGER fail_delete BEFORE DELETE ON dependencies BEGIN SELECT RAISE(ABORT, 'delete refused'); END`},
		{"sessions", `CREATE TRIGGER fail_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'delete refused'); END`},
		{"agents", `CREATE TRIGGER fail_delete BEFORE DELETE ON agents BEGIN SELECT RAISE(ABORT, 'delete refused'); END`},
		{"nodes", `CREATE TRIGGER fail_delete BEFORE DELETE ON nodes BEGIN SELECT RAISE(ABORT, 'delete refused'); END`},
		{"sequences", `CREATE TRIGGER fail_delete BEFORE DELETE ON sequences BEGIN SELECT RAISE(ABORT, 'delete refused'); END`},
	}
	for _, tt := range tests {
		t.Run(tt.table, func(t *testing.T) {
			s := literalTestStore(t)
			seedLiteralDeleteTables(t, s)
			_, err := s.writeDB.Exec(tt.trigger)
			require.NoError(t, err)
			tx, err := s.writeDB.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			err = clearAllTables(context.Background(), tx)
			require.ErrorContains(t, err, "clear table "+tt.table)
			require.ErrorContains(t, err, "delete refused")
			require.NoError(t, tx.Rollback())
			assertLiteralDeleteTables(t, s)
		})
	}
}

func seedLiteralDeleteTables(t *testing.T, s *Store) {
	t.Helper()
	statements := []string{
		`INSERT INTO nodes (id, project, depth, seq, title, node_type, status, priority, weight, created_at, updated_at, content_hash) VALUES ('TEST-1', 'TEST', 0, 1, 'one', 'story', 'open', 2, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'hash')`,
		`INSERT INTO nodes (id, project, depth, seq, title, node_type, status, priority, weight, created_at, updated_at, content_hash) VALUES ('TEST-2', 'TEST', 0, 2, 'two', 'story', 'open', 2, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'hash')`,
		`INSERT INTO dependencies (from_id, to_id, dep_type, created_at) VALUES ('TEST-1', 'TEST-2', 'blocks', '2026-01-01T00:00:00Z')`,
		`INSERT INTO agents (agent_id, project, last_heartbeat) VALUES ('alice', 'TEST', '2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions (id, agent_id, project, started_at) VALUES ('session', 'alice', 'TEST', '2026-01-01T00:00:00Z')`,
		`INSERT INTO sequences (key, value) VALUES ('TEST:', 2)`,
	}
	for _, stmt := range statements {
		_, err := s.writeDB.Exec(stmt)
		require.NoError(t, err)
	}
}

func assertLiteralDeleteTables(t *testing.T, s *Store) {
	t.Helper()
	for _, query := range []string{`SELECT COUNT(*) FROM dependencies`, `SELECT COUNT(*) FROM sessions`, `SELECT COUNT(*) FROM agents`, `SELECT COUNT(*) FROM sequences`} {
		var count int
		require.NoError(t, s.readDB.QueryRow(query).Scan(&count))
		require.Equal(t, 1, count)
	}
	var nodes int
	require.NoError(t, s.readDB.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&nodes))
	require.Equal(t, 2, nodes)
}

func TestClearAllTables_SuccessEmptiesAllTables(t *testing.T) {
	s := literalTestStore(t)
	seedLiteralDeleteTables(t, s)
	tx, err := s.writeDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, clearAllTables(context.Background(), tx))
	require.NoError(t, tx.Commit())
	for _, query := range []string{`SELECT COUNT(*) FROM dependencies`, `SELECT COUNT(*) FROM sessions`, `SELECT COUNT(*) FROM agents`, `SELECT COUNT(*) FROM nodes`, `SELECT COUNT(*) FROM sequences`} {
		var count int
		require.NoError(t, s.readDB.QueryRow(query).Scan(&count))
		require.Zero(t, count)
	}
}

func TestDetectLWWOutcome_NonUpdateOperationsRetainBehavior(t *testing.T) {
	s := literalTestStore(t)
	tests := []struct {
		op      model.OpType
		payload any
		field   string
	}{
		{model.OpSetAcceptance, model.SetAcceptancePayload{}, "acceptance"},
		{model.OpSetPrompt, model.SetPromptPayload{}, "prompt"},
	}
	for _, tt := range tests {
		t.Run(string(tt.op), func(t *testing.T) {
			tx, err := s.writeDB.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			defer func() { require.NoError(t, tx.Rollback()) }()
			e := makeApplyEvent(t, tt.op, "TEST-1", "alice", 1, tt.payload)
			outcome, err := detectLWWOutcome(context.Background(), tx, e)
			require.NoError(t, err)
			require.Equal(t, tt.field, outcome.FieldName)
			require.False(t, outcome.HasPrior)
		})
	}
}
