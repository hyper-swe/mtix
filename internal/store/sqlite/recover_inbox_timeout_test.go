// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// warmupIntegrityTimeout bounds the warm-up integrity checks only. It is
// deliberately far above the 5 second production limit so that a slow
// -race host cannot fail the warm-up (MTIX-112.54); the measured limit
// below is what exercises the real timeout path.
const warmupIntegrityTimeout = time.Minute

// FR-19.1: exercise the real check with an injected budget, not a host-speed threshold.
func TestRecoverInboxJournal_RealIntegrityTimeout_ExplainsUnprovenCoverage(t *testing.T) {
	s := exchangeSnapshotStore(t)
	insertGuardMail(t, s, "sender")
	_, err := s.WriteDB().Exec(`INSERT INTO agent_inbox_ack(agent_id,event_seq,acked_at) SELECT 'worker',rowid,'2026-11-05T00:00:00Z' FROM sync_events WHERE event_id='guard-mail'`)
	require.NoError(t, err)
	_, err = s.WriteDB().Exec(`CREATE TABLE slow_integrity(work INTEGER NOT NULL)`)
	require.NoError(t, err)
	_, err = s.WriteDB().Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<50000) INSERT INTO slow_integrity SELECT x FROM n`)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err = s.WriteDB().Exec(fmt.Sprintf("CREATE INDEX slow_integrity_%d ON slow_integrity(work)", i))
		require.NoError(t, err)
	}
	// Measure a warmed real check on this host, then give the same work at most
	// one hundredth of its fastest measured duration. Production still uses 5s (inboxIntegrityTimeout).
	fastest := time.Hour
	for i := 0; i < 3; i++ {
		start := time.Now()
		require.NoError(t, checkInboxIntegrity(t.Context(), s.readDB.rawDB, warmupIntegrityTimeout))
		fastest = min(fastest, time.Since(start))
	}
	limit := min(fastest/100, time.Millisecond)
	require.Positive(t, limit)
	require.ErrorIs(t, checkInboxIntegrity(t.Context(), s.readDB.rawDB, limit), context.DeadlineExceeded)
	res := &RecoverResult{}
	sourceBefore, statErr := os.Stat(s.dbPath)
	require.NoError(t, statErr)
	journal := recoverInboxJournal(t.Context(), s.dbPath, nil, res, limit, sourceBefore)
	require.True(t, res.InboxIncomplete)
	require.Len(t, journal.Events, 1)
	require.False(t, journal.Events[0].Acked)
	require.Empty(t, journal.Events[0].AckedAt)
	notes := strings.Join(res.Notes, " ")
	require.Contains(t, notes, "integrity_check did not finish")
	require.Contains(t, notes, "could not be proven")
	require.Contains(t, notes, "mtix export")
	require.NotContains(t, notes, "partially unreadable or unavailable")
	require.NotContains(t, notes, "damaged database")
}
