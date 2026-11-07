// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// These import-shaped edits change content without adding a journal event.
// Every original event is on the real hub; tasks.json matches the edited store.
func TestEnrolWorktree_RealHub_AddresseeDifference_Refuses_PG(t *testing.T) {
	for _, history := range []struct{ name, config string }{{"history off", ""}, {"history on", historyConfig}} {
		for _, recipient := range []string{"lane-b", ""} {
			t.Run("recipient="+recipient+"/"+history.name, func(t *testing.T) {
				f := newEnrolPGFixture(t)
				st := f.open(t)
				_, err := st.WriteDB().ExecContext(t.Context(),
					`UPDATE nodes SET annotations = json_set(annotations, '$[0].addressee', ?) WHERE id = ?`, recipient, "TEST-1")
				require.NoError(t, err)
				require.NoError(t, st.Close())
				f.writeBoardAs(t, history.config)

				requireReplicatedEnrolRefusal(t, f, "annotations")
			})
		}
	}
}

func TestEnrolWorktree_RealHub_ClosedAtDifference_Refuses_PG(t *testing.T) {
	for _, history := range []struct{ name, config string }{{"history off", ""}, {"history on", historyConfig}} {
		for _, tc := range []struct {
			name     string
			complete bool
			stamp    any
		}{
			{"done time changed", true, "2026-06-02T03:04:05.123456789Z"},
			{"done time cleared", true, nil},
			{"open time added", false, "2026-06-02T03:04:05.123456789Z"},
		} {
			t.Run(tc.name+"/"+history.name, func(t *testing.T) {
				f := newEnrolPGFixture(t)
				if tc.complete {
					f.ownWork(t, func(ctx context.Context, st *sqlite.Store) {
						require.NoError(t, st.ClaimNode(ctx, "TEST-1", "lane-a"))
						require.NoError(t, st.TransitionStatus(ctx, "TEST-1", model.StatusInProgress, "start", "lane-a"))
						require.NoError(t, st.TransitionStatus(ctx, "TEST-1", model.StatusDone, "finished", "lane-a"))
					})
					require.NotNil(t, replicatedEnrolNode(t, f).ClosedAt)
				}
				st := f.open(t)
				_, err := st.WriteDB().ExecContext(t.Context(), `UPDATE nodes SET closed_at = ? WHERE id = ?`, tc.stamp, "TEST-1")
				require.NoError(t, err)
				require.NoError(t, st.Close())
				f.writeBoardAs(t, history.config)

				requireReplicatedEnrolRefusal(t, f, "closed_at")
			})
		}
	}
}

func replicatedEnrolNode(t *testing.T, f *enrolPGFixture) *model.Node {
	t.Helper()
	st := f.open(t)
	node, err := st.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.NoError(t, st.Close())
	return node
}

func requireReplicatedEnrolRefusal(t *testing.T, f *enrolPGFixture, field string) {
	t.Helper()
	before := replicatedEnrolNode(t, f)
	boardPath := filepath.Join(f.root, ".mtix", "tasks.json")
	board, err := os.ReadFile(boardPath)
	require.NoError(t, err)

	_, err = f.enrol()

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardHubDiffers, refusal.Reason)
	assert.Contains(t, refusal.Diff["TEST-1"], field)
	assert.Empty(t, f.enrolled)
	after := replicatedEnrolNode(t, f)
	assert.Equal(t, before.Annotations, after.Annotations)
	assert.Equal(t, before.ClosedAt, after.ClosedAt)
	assert.Equal(t, before.Status, after.Status)
	assert.Equal(t, board, fileBytes(t, boardPath))
	asides, err := filepath.Glob(filepath.Join(f.root, ".mtix", "data.pre-enrol-*"))
	require.NoError(t, err)
	assert.Empty(t, asides)
}
