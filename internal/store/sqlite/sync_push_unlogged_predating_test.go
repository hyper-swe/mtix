// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestPredatingDeletesNamedBy_ListsThePendingDeletesBeforeTheLoggedCreation:
// before push releases held events of unlogged tasks (MTIX-112.17.16.37,
// lead ruling r2 2), it reads the pending deletes, not held, of every task
// on the lines of those events that arrived before the task's logged
// creation; those it holds as retired.
func TestPredatingDeletesNamedBy_ListsThePendingDeletesBeforeTheLoggedCreation(t *testing.T) {
	ctx := context.Background()
	title := "retitled"
	edit := func(t *testing.T, s *sqlite.Store, raw *sql.DB, id string) string {
		require.NoError(t, s.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title}))
		return lastEventID(t, raw, id, model.OpUpdateField)
	}
	del := func(t *testing.T, s *sqlite.Store, raw *sql.DB, id string) string {
		require.NoError(t, s.DeleteNode(ctx, id, true, "alice"))
		return lastEventID(t, raw, id, model.OpDelete)
	}
	undelete := func(t *testing.T, raw *sql.DB) {
		_, err := raw.Exec(`UPDATE nodes SET deleted_at = NULL WHERE id IN ('MTIX-1', 'MTIX-1.1')`)
		require.NoError(t, err)
	}
	tests := []struct {
		name string
		// setup returns the released held events and the deletes to list.
		setup func(t *testing.T, s *sqlite.Store, raw *sql.DB) (released, want []string)
	}{
		{"the task's own delete before the pulled creation", func(t *testing.T, s *sqlite.Store, raw *sql.DB) ([]string, []string) {
			e := edit(t, s, raw, "MTIX-1")
			d := del(t, s, raw, "MTIX-1")
			undelete(t, raw)
			pullCreation(t, raw, "MTIX-1")
			return []string{e}, []string{d}
		}},
		// The child's own creation is in the log before its cascade delete,
		// so that delete does not predate it and is not listed.
		{"the parent's delete, named by the child's event", func(t *testing.T, s *sqlite.Store, raw *sql.DB) ([]string, []string) {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			e := edit(t, s, raw, "MTIX-1.1")
			d := del(t, s, raw, "MTIX-1")
			undelete(t, raw)
			pullCreation(t, raw, "MTIX-1")
			return []string{e}, []string{d}
		}},
		{"a held delete is not listed", func(t *testing.T, s *sqlite.Store, raw *sql.DB) ([]string, []string) {
			e := edit(t, s, raw, "MTIX-1")
			holdAsUnlogged(t, s, del(t, s, raw, "MTIX-1"))
			undelete(t, raw)
			pullCreation(t, raw, "MTIX-1")
			return []string{e}, nil
		}},
		{"a delete after the logged creation is not listed", func(t *testing.T, s *sqlite.Store, raw *sql.DB) ([]string, []string) {
			e := edit(t, s, raw, "MTIX-1")
			pullCreation(t, raw, "MTIX-1")
			del(t, s, raw, "MTIX-1")
			return []string{e}, nil
		}},
		{"another task's delete is not listed", func(t *testing.T, s *sqlite.Store, raw *sql.DB) ([]string, []string) {
			e := edit(t, s, raw, "MTIX-1")
			del(t, s, raw, "MTIX-3")
			pullCreation(t, raw, "MTIX-3")
			return []string{e}, nil
		}},
		{"no released event", func(t *testing.T, s *sqlite.Store, raw *sql.DB) ([]string, []string) {
			del(t, s, raw, "MTIX-1")
			pullCreation(t, raw, "MTIX-1")
			return nil, nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := unloggedFixture(t)
			released, want := tt.setup(t, s, raw)
			got, err := s.PredatingDeletesNamedBy(ctx, released)
			require.NoError(t, err)
			require.ElementsMatch(t, want, got)
		})
	}
}
