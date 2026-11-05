// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Checksummed input still has to satisfy the journal's domain constraints.
func TestValidateInboxJournal_InvalidContents_RejectsInput(t *testing.T) {
	cases := []struct {
		name   string
		change func(*inboxJournalEvent)
	}{
		{"empty identity", func(e *inboxJournalEvent) { e.EventID = "" }},
		{"bad node", func(e *inboxJournalEvent) { e.NodeID = "../../elsewhere" }},
		{"bad project", func(e *inboxJournalEvent) { e.Project = "invalid project" }},
		{"missing recipient", func(e *inboxJournalEvent) { e.Payload = `{"body":"mail"}` }},
		{"bad payload", func(e *inboxJournalEvent) { e.Payload = `{` }},
		{"bad time", func(e *inboxJournalEvent) { e.CreatedAt = "broken" }},
		{"missing time", func(e *inboxJournalEvent) { e.CreatedAt = "" }},
		{"unacked timestamp", func(e *inboxJournalEvent) { e.AckedAt = e.CreatedAt }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := inboxJournalEvent{EventID: "event-1", Author: "sender", Project: "TEST", NodeID: "TEST-1", Payload: `{"to":"worker","body":"mail"}`, CreatedAt: "2026-11-05T00:00:00Z"}
			tc.change(&e)
			j := &InboxJournal{Version: 1, Events: []inboxJournalEvent{e}}
			var err error
			j.Checksum, err = inboxJournalChecksum(j)
			require.NoError(t, err)
			require.Error(t, validateInboxJournal(j))
		})
	}
}

func TestValidateInboxJournal_DuplicateIdentityOrInvalidCoverage_RejectsInput(t *testing.T) {
	e := inboxJournalEvent{EventID: "event-1", Author: "sender", Project: "TEST", NodeID: "TEST-1", Payload: `{"to":"worker","body":"mail"}`, CreatedAt: "2026-11-05T00:00:00Z", Acked: true}
	j := &InboxJournal{Version: 1, Events: []inboxJournalEvent{e}}
	var err error
	j.Checksum, err = inboxJournalChecksum(j)
	require.NoError(t, err)
	require.NoError(t, validateInboxJournal(j), "compacted coverage has no invented timestamp")
	j.Events = append(j.Events, e)
	j.Checksum, err = inboxJournalChecksum(j)
	require.NoError(t, err)
	require.ErrorContains(t, validateInboxJournal(j), "duplicate")
	e.AckedAt = "broken"
	require.ErrorContains(t, validateInboxEvent(e), "timestamp")
}
