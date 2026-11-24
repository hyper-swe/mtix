// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func deriveTicket(t *testing.T, s *sqlite.Store, id string) (ticket model.DecisionTicket, err error) {
	t.Helper()
	err = s.WithTx(t.Context(), func(tx *sql.Tx) error {
		var e error
		ticket, e = sqlite.DeriveDecisionTicket(t.Context(), tx, id)
		return e
	})
	return ticket, err
}

func TestDeriveDecisionTicket_Ancestry(t *testing.T) {
	s, db := mutationTestStore(t)
	sqlite.SeedDecisionDeleteTestMachineHash(t, db)
	mustCreateNode(t, s, "MTIX-1", "")
	require.NoError(t, s.DeleteNode(t.Context(), "MTIX-1", false, "alice"))
	const leaf = "MTIX-1.1.1"
	mustCreateNode(t, s, leaf, "")
	_, err := db.Exec("UPDATE nodes SET uid=NULL WHERE id=?", leaf)
	require.NoError(t, err)
	ticket, err := deriveTicket(t, s, leaf)
	require.NoError(t, err)
	require.True(t, model.IsBackfillUID(ticket.UID))
	var uid, root string
	require.NoError(t, db.QueryRow("SELECT uid FROM nodes WHERE id=?", leaf).Scan(&uid))
	require.NoError(t, db.QueryRow("SELECT uid FROM nodes WHERE id='MTIX-1'").Scan(&root))
	require.Equal(t, model.DecisionTicket{UID: uid, Ancestors: []string{"", root}, Paths: []string{leaf, "MTIX-1.1", "MTIX-1"}, PathMatch: []bool{true, true, true}}, ticket)
	again, err := deriveTicket(t, s, leaf)
	require.NoError(t, err)
	require.Equal(t, ticket, again)
	_, err = deriveTicket(t, s, "MTIX-99")
	require.ErrorIs(t, err, model.ErrNotFound)
	_, err = db.Exec("UPDATE nodes SET uid=NULL WHERE id=?", leaf)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TRIGGER fail_uid BEFORE UPDATE OF uid ON nodes BEGIN SELECT RAISE(ABORT,'blocked backfill'); END")
	require.NoError(t, err)
	_, err = deriveTicket(t, s, leaf)
	require.ErrorContains(t, err, "blocked backfill")
}

func TestDeriveDecisionTicket_DepthCap(t *testing.T) {
	for _, depth := range []int{0, 64, 65} {
		s, db := mutationTestStore(t)
		sqlite.SeedDecisionDeleteTestMachineHash(t, db)
		id := "MTIX-1" + strings.Repeat(".1", depth)
		mustCreateNode(t, s, id, "")
		ticket, err := deriveTicket(t, s, id)
		require.NoError(t, err)
		require.NotNil(t, ticket.Ancestors)
		require.Len(t, ticket.Ancestors, min(depth, 64))
		require.Len(t, ticket.PathMatch, len(ticket.Ancestors)+1)
		require.Len(t, ticket.Paths, len(ticket.PathMatch))
		for i, p := range ticket.Paths {
			require.True(t, ticket.PathMatch[i])
			require.Equal(t, id, p)
			id = model.ParseIDParent(id)
		}
		require.Equal(t, depth > 64, ticket.Truncated)

		// Exercise the merged v1 authoring decoder with empty and capped chains.
		raw, err := json.Marshal(model.DecisionPayload{
			DecisionHeader: model.DecisionHeader{V: 1, Kind: "request", RequestKey: "raise"},
			DecisionRequest: &model.DecisionRequest{
				Question: "Proceed?",
				Ticket:   ticket,
				Asker:    model.DecisionActor{Kind: "agent"},
			},
		})
		require.NoError(t, err)
		decoded, err := model.DecodeDecision(raw, "alice", true)
		require.NoError(t, err)
		require.Equal(t, ticket, decoded.Payload.Ticket)
	}
}

func TestDeriveDecisionTicket_HeldDelete(t *testing.T) {
	marked, err := model.NewBackfillUID()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, path, node, deleteUID, localUID string
		cascade, ancestor, match              bool
	}{
		{"legacy self", "", "MTIX-1.1", "", "new", false, false, false},
		{"immutable path wins", "MTIX-1.1", "MOVED-1", marked, "new", false, false, false},
		{"renamed envelope ignored", "MOVED-1", "MTIX-1.1", "", "new", false, false, true},
		{"same uid", "MTIX-1.1", "MOVED-1", marked, marked, false, false, true},
		{"divergent ordinary uids", "MTIX-1.1", "MOVED-1", "old", "new", false, false, true},
		{"local backfill", "MTIX-1.1", "MOVED-1", "old", marked, false, false, false},
		{"legacy ancestor", "", "MTIX-1", "", "new", false, true, true},
		{"uidless delete and missing ancestor", "MTIX-1", "MOVED-1", "", "new", true, true, false},
		{"uidless ancestor delete without cascade", "MTIX-1", "MOVED-1", "", "new", false, true, true},
		{"missing ancestor cascade", "MTIX-1", "MOVED-1", "old", "new", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := mutationTestStore(t)
			sqlite.SeedDecisionDeleteTestMachineHash(t, db)
			mustCreateNode(t, s, "MTIX-1.1", "")
			_, err := db.Exec("UPDATE nodes SET uid=? WHERE id='MTIX-1.1'", tc.localUID)
			require.NoError(t, err)
			payload := []byte("{}")
			if tc.path != "" {
				payload, err = json.Marshal(map[string]any{"path": tc.path, "cascade": tc.cascade})
				require.NoError(t, err)
			}
			_, err = db.Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,uid,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
 VALUES('d','MTIX',?,'delete',?,?,0,1,'{}','alice','','2026-11-01')`, tc.node, string(payload), tc.deleteUID)
			require.NoError(t, err)
			ticket, err := deriveTicket(t, s, "MTIX-1.1")
			require.NoError(t, err)
			want := []bool{tc.match, true}
			if tc.ancestor {
				want = []bool{true, tc.match}
			}
			require.Equal(t, want, ticket.PathMatch)
		})
	}
}

func TestDeriveDecisionTicket_FailedRaiseRollsBackUID(t *testing.T) {
	s, db := mutationTestStore(t)
	sqlite.SeedDecisionDeleteTestMachineHash(t, db)
	mustCreateNode(t, s, "MTIX-1", "")
	_, err := db.Exec("UPDATE nodes SET uid=NULL WHERE id='MTIX-1'")
	require.NoError(t, err)
	refused := errors.New("raise refused")
	err = s.WithTx(t.Context(), func(tx *sql.Tx) error {
		ticket, deriveErr := sqlite.DeriveDecisionTicket(t.Context(), tx, "MTIX-1")
		if deriveErr != nil {
			return deriveErr
		}
		require.True(t, model.IsBackfillUID(ticket.UID))
		return refused
	})
	require.ErrorIs(t, err, refused)
	var uid sql.NullString
	require.NoError(t, db.QueryRow("SELECT uid FROM nodes WHERE id='MTIX-1'").Scan(&uid))
	require.False(t, uid.Valid)
}

// A damaged store must never produce a partly trusted chain or silently
// grant path fallback. Faults are confined to the raise transaction.
func TestDeriveDecisionTicket_StorageFailureRefusesSnapshot(t *testing.T) {
	s, db := mutationTestStore(t)
	sqlite.SeedDecisionDeleteTestMachineHash(t, db)
	const id = "MTIX-1.1"
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, id, "MTIX-1")
	require.NoError(t, s.DeleteNode(t.Context(), "MTIX-1", false, "alice"))
	for _, tc := range []struct {
		name, fault, want string
	}{
		{"unreadable ticket", "ALTER TABLE nodes RENAME TO unavailable_nodes", "read decision ticket"},
		{"unreadable ancestor", `ALTER TABLE nodes RENAME TO stored_nodes;
		  CREATE VIEW nodes AS SELECT id, CASE WHEN id='MTIX-1' THEN json_extract('broken','$')
		    ELSE uid END AS uid FROM stored_nodes`, "read decision ancestor"},
		{"lost backfill", `UPDATE nodes SET uid=NULL WHERE id='MTIX-1.1';
		  CREATE TRIGGER lose_uid AFTER UPDATE OF uid ON nodes WHEN NEW.uid IS NOT NULL
		    BEGIN UPDATE nodes SET uid=NULL WHERE id=NEW.id; END`, "still lacks a uid"},
		{"missing journal", "ALTER TABLE sync_events RENAME TO unavailable_journal", "held decision delete"},
		{"unreadable delete", "UPDATE sync_events SET payload='broken' WHERE op_type='delete'", "held decision delete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.WithTx(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(t.Context(), tc.fault)
				require.NoError(t, err)
				ticket, err := sqlite.DeriveDecisionTicket(t.Context(), tx, id)
				require.Equal(t, model.DecisionTicket{}, ticket)
				return err
			})
			require.ErrorContains(t, err, tc.want)
		})
	}
}
