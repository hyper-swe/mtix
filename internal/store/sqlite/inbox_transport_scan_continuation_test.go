// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecover_ScanErrorAfterGap_KeepsLaterReadableRows(t *testing.T) {
	source, raw := retentionStore(t)
	addressTo(t, source, "recipient", "PROJ-1")
	for _, s := range []string{
		`DELETE FROM sync_events`,
		`CREATE TABLE j2 AS SELECT * FROM sync_events`,
		`DROP TABLE sync_events`,
		`ALTER TABLE j2 RENAME TO sync_events`,
	} {
		_, err := raw.Exec(s)
		require.NoError(t, err)
	}
	ins := func(seq int, created any) {
		_, err := raw.Exec(`INSERT INTO sync_events(rowid,event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?,?,'PROJ','PROJ-1','comment','{"to":"recipient","body":"m"}',0,1,'{}','sender','',?)`, seq, fmt.Sprintf("g-%d", seq), created)
		require.NoError(t, err)
	}
	var exp []string
	for s := 1; s <= 5; s++ {
		ins(s, "2026-11-05T00:00:00Z")
		exp = append(exp, fmt.Sprintf("g-%d", s))
	}
	ins(30000, nil)
	for s := 30001; s <= 30020; s++ {
		ins(s, "2026-11-05T00:00:00Z")
		exp = append(exp, fmt.Sprintf("g-%d", s))
	}
	var path string
	require.NoError(t, raw.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path))
	result := recoverDamageMail(t, path)
	var got []string
	for _, event := range result.Export.InboxJournal.Events {
		got = append(got, event.EventID)
	}
	require.ElementsMatch(t, exp, got)
	require.True(t, result.InboxIncomplete)
}
