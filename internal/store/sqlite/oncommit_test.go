// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-53: the store must support MULTIPLE post-commit callbacks so a
// long-running server can wire BOTH the tasks.json mirror export AND hook
// dispatch off the same commit choke point without one clobbering the other.

func writeOnce(t *testing.T, s *sqlite.Store) {
	t.Helper()
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`INSERT INTO meta(key, value) VALUES('mtix53.probe','1')
			 ON CONFLICT(key) DO UPDATE SET value = value`)
		return err
	}))
}

// TestAddOnCommit_RunsAllCallbacks: two callbacks registered via AddOnCommit both
// fire after a committed write.
func TestAddOnCommit_RunsAllCallbacks(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	var a, b int
	s.AddOnCommit(func() { a++ })
	s.AddOnCommit(func() { b++ })

	writeOnce(t, s)

	require.Equal(t, 1, a, "first callback ran once")
	require.Equal(t, 1, b, "second callback ran once")
}

// TestAddOnCommit_CoexistsWithSetOnCommit: SetOnCommit (the mirror exporter's
// API) and AddOnCommit (hook dispatch) both fire — adding one does not drop the
// other.
func TestAddOnCommit_CoexistsWithSetOnCommit(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	var setRan, addRan int
	s.SetOnCommit(func() { setRan++ })
	s.AddOnCommit(func() { addRan++ })

	writeOnce(t, s)

	require.Equal(t, 1, setRan, "SetOnCommit callback still fires")
	require.Equal(t, 1, addRan, "AddOnCommit callback also fires")
}
