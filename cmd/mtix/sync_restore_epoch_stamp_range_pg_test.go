// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestLeastPrivilegeRole_HeldStampOutsideHubRange_NotEarlier: a create the
// hub holds with a restore epoch below 0 or above the hub's current epoch
// is not earlier than the current epoch. The recorder records nothing for another node's create of
// that number, and a push of such a create renumbers it (MTIX-95.1.7).
func TestLeastPrivilegeRole_HeldStampOutsideHubRange_NotEarlier(t *testing.T) {
	grants := documentedSmallTeamGrants(t) // read before initTestApp changes directory
	initTestApp(t)
	h := newLeastPrivilegeHub(t, grants)
	h.push(t, h.event("MTIX-6.1", "alice", model.OpCreateNode, `{"title":"low"}`),
		h.event("MTIX-6.2", "alice", model.OpCreateNode, `{"title":"high"}`))
	_, err := h.ownerPool.MarkRestored(h.ctx)
	require.NoError(t, err)
	h.f.exec(`UPDATE hub_data.sync_events SET restore_epoch = -1 WHERE node_id = 'MTIX-6.1'`)
	h.f.exec(`UPDATE hub_data.sync_events SET restore_epoch = 2 WHERE node_id = 'MTIX-6.2'`)

	for i, node := range []string{"MTIX-6.1", "MTIX-6.2"} {
		id := []string{"0193fa00-0000-7000-8000-00000000f0c1", "0193fa00-0000-7000-8000-00000000f0c2"}[i]
		require.Falsef(t, h.callRecorder(t, recordCall{"MTIX", node, id, id, 1}), "%s records nothing", node)
	}
	require.Empty(t, h.collisionRows(t))
	for _, node := range []string{"MTIX-6.1", "MTIX-6.2"} {
		_, renumbers, collisions := h.push(t, h.event(node, "bob", model.OpCreateNode, `{"title":"b"}`))
		require.Emptyf(t, collisions, "%s is not a restore collision", node)
		require.Lenf(t, renumbers, 1, "%s renumbers", node)
	}
	require.Empty(t, h.collisionRows(t))
}

// TestDoctorSchemaCurrent_CreateStampsOutsideHubRange_WarnsWithOwnerUpdate:
// create events the hub holds with a restore epoch below 0 or above its
// current epoch make the schema current check warn, with their number, the
// current epoch and the table owner's UPDATE, its schema quoted
// server-side, which sets each to the current epoch. Run as printed, it
// clears the check and changes no other event; a stamp from 0 to the
// current epoch, and a non-create event's stamp, are not counted
// (MTIX-95.1.7).
func TestDoctorSchemaCurrent_CreateStampsOutsideHubRange_WarnsWithOwnerUpdate(t *testing.T) {
	initTestApp(t)
	h := newSchemaHub(t, `Hub "Q" x`)
	for _, e := range [][3]string{{"e1", "create_node", "-1"}, {"e2", "create_node", "0"}, {"e3", "create_node", "2"},
		{"e4", "create_node", "3"}, {"e5", "comment", "-5"}} {
		h.f.ddl(`INSERT INTO %I.sync_events (event_id, project_prefix, node_id, uid, op_type, payload, wall_clock_ts,
			lamport_clock, vector_clock, author_id, author_machine_hash)
			VALUES (%L, 'MTIX', %L, %L, %L, '{"title":"x"}', 1, 1, '{"w":1}', 'w', '0123456789abcdef')`,
			h.schema, e[0], "MTIX-7."+e[0], e[0], e[1])
		h.f.ddl(`UPDATE %I.sync_events SET restore_epoch = %s WHERE event_id = %L`, h.schema, e[2], e[0])
	}
	h.f.ddl(`UPDATE %I.sync_hub_state SET restore_epoch = 2`, h.schema)

	schema := h.ident(h.schema)
	update := "UPDATE " + schema + ".sync_events SET restore_epoch = (SELECT s.restore_epoch FROM " + schema +
		".sync_hub_state s WHERE s.id) WHERE op_type = 'create_node' AND (restore_epoch < 0 OR restore_epoch > " +
		"(SELECT s.restore_epoch FROM " + schema + ".sync_hub_state s WHERE s.id));"
	pass, warn, detail, fix, err := schemaCurrentCheck(t, h.syncDSN)
	require.NoError(t, err, "a WARN keeps the doctor's exit code 0")
	require.True(t, pass, detail)
	require.True(t, warn, detail)
	require.Contains(t, detail, "create events stamped with a restore epoch outside 0 to 2, the hub's current epoch: 2")
	require.Equal(t, "as the table owner ("+h.owner+"): "+update, fix)

	printed := strings.TrimPrefix(fix, "as the table owner ("+h.owner+"): ")
	require.NoError(t, h.f.tryAs(h.owner, printed), "the printed UPDATE runs as printed")
	h.requireSchemaCurrentClean(t)
	require.Equal(t, []string{"e1 2", "e2 0", "e3 2", "e4 2", "e5 -5"}, h.stamps(t))
}

// stamps lists each event's id and restore epoch, as the syncing role
// reads them through its search_path.
func (h *schemaHub) stamps(t *testing.T) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := transport.New(ctx, h.syncDSN, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	defer pool.Close()
	rows, err := pool.Inner().Query(ctx, `SELECT event_id || ' ' || restore_epoch::text FROM sync_events ORDER BY 1`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}
