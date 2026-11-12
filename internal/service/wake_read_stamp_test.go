// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// lastReadRows counts the last-read rows of the fixture's board.
func (f *scanFixture) lastReadRows() int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.store.WriteDB().QueryRow(`SELECT COUNT(*) FROM agent_inbox_reads`).Scan(&n))
	return n
}

// Reading an inbox for the wake scanner is not the agent reading it: neither
// Scan nor Gate stamps last read, whatever session value the caller's context
// carries (a bare context included).
func TestWakeScan_ScanAndGate_DoNotStampLastRead(t *testing.T) {
	f := newScanFixture(t)
	f.addEvent("sender")
	_, err := f.store.WriteDB().Exec(`DELETE FROM agent_inbox_reads`)
	require.NoError(t, err)
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.NotEmpty(t, f.adapter.seqs(), "premise: the scan read the inbox and sent")
	_, err = f.scanner(always).Gate(t.Context(), f.key)
	require.NoError(t, err)
	require.Zero(t, f.lastReadRows(), "a wake scan is not the agent reading")
}

func TestReevaluate_DoesNotStampLastRead(t *testing.T) {
	f, held, _ := heldWithOwnAbove(t)
	_, err := f.store.WriteDB().Exec(`DELETE FROM agent_inbox_reads`)
	require.NoError(t, err)
	require.NoError(t, f.d.Reevaluate(t.Context(), f.key.AgentID))
	require.Equal(t, []int64{held}, f.adapter.seqs(), "premise: re-evaluation read the inbox and sent")
	require.Zero(t, f.lastReadRows(), "re-evaluation is not the agent reading")
}
