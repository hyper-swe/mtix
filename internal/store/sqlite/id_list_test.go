// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestHexIDList_EveryHostileID_ComesBackByteExactThroughTheSubquery(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ids.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`CREATE TABLE ids (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	var ids []string
	for _, fx := range testutil.HostileIDs() {
		ids = append(ids, fx.Value)
		_, err = db.Exec(`INSERT INTO ids (id) VALUES (?)`, fx.Value)
		require.NoError(t, err)
	}
	list, err := sqlite.HexIDListForTest(ids)
	require.NoError(t, err)

	// The subquery text is the one the statements spell out.
	require.Equal(t, "SELECT CAST(unhex(value) AS TEXT) FROM json_each(?)", sqlite.IDListSubqueryForTest)
	rows, err := db.Query(`SELECT id FROM ids WHERE id IN (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?))`, list)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	got := map[string]bool{}
	for rows.Next() {
		var b []byte
		require.NoError(t, rows.Scan(&b))
		got[string(b)] = true
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, len(ids), "every id, including NUL and invalid UTF-8, is selected by its exact bytes")
	for _, id := range ids {
		require.True(t, got[id], "id %q selected", id)
	}
}

func TestHexIDList_Empty_IsAnEmptyJSONArray(t *testing.T) {
	list, err := sqlite.HexIDListForTest(nil)
	require.NoError(t, err)
	require.Equal(t, "[]", list)
	require.False(t, strings.Contains(list, "null"))
}

// Rule 1b: a test may prefix EXPLAIN QUERY PLAN to a constant statement. The
// id list keeps the primary-key probe, so the cost is that of the ids asked
// about, not of the table.
func TestIDListSubquery_UsesThePrimaryKeyIndex(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "plan.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`CREATE TABLE ids (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT id FROM ids WHERE id IN (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?))`, "[]")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	require.NoError(t, rows.Err())
	joined := strings.Join(plan, "\n")
	require.Contains(t, joined, "SEARCH ids USING", joined)
	require.NotContains(t, joined, "SCAN ids", joined)
}
