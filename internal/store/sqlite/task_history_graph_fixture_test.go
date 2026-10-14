// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const graphFixtureHead = `package x

import ("context"; "database/sql")

type S struct{}

func (s *S) withHistoryTx(ctx context.Context, sc int, fn func(tx *sql.Tx) error) error { return nil }
func (s *S) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error { return nil }

func leaf() string { return "UPDATE nodes SET a = 1" }

func (s *S) Good(ctx context.Context) error {
	return s.withHistoryTx(ctx, 0, func(tx *sql.Tx) error { _ = leaf(); return nil })
}
`

// TestTaskHistoryCensus_CallGraph_FollowsEveryWayToReachALeaf proves the walk
// is not fooled by a reference that is not a direct call (MTIX-95.31.19.2.2,
// .3): a function value, a callback and a package-level func literal.
func TestTaskHistoryCensus_CallGraph_FollowsEveryWayToReachALeaf(t *testing.T) {
	tests := []struct {
		name     string
		extra    string
		recorded bool
	}{
		{"only recorded callers", "", true},
		{"direct plain call", "func (s *S) Bad(ctx context.Context) error { return s.WithTx(ctx, func(tx *sql.Tx) error { _ = leaf(); return nil }) }\n", false},
		{"function value then call", "func (s *S) Bad(ctx context.Context) error { return s.WithTx(ctx, func(tx *sql.Tx) error { f := leaf; _ = f(); return nil }) }\n", false},
		{"callback passed to a helper", "func run(fn func() string) string { return fn() }\nfunc (s *S) Bad(ctx context.Context) error { return s.WithTx(ctx, func(tx *sql.Tx) error { _ = run(leaf); return nil }) }\n", false},
		{"package-level func literal", "var viaVar = func() string { return leaf() }\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "f.go"), []byte(graphFixtureHead+tt.extra), 0o600))
			g := buildHistoryGraph(t, dir)
			assert.Equal(t, tt.recorded, g.recorded("leaf", map[string]bool{}))
		})
	}
}
