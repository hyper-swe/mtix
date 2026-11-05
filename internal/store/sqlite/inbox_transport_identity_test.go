// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateInboxJournal_InvalidActors_RejectsChecksummedInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*inboxJournalEvent)
	}{
		{"empty author", func(e *inboxJournalEvent) { e.Author = "" }},
		{"recipient length", func(e *inboxJournalEvent) { e.Payload = `{"to":"` + strings.Repeat("w", 65) + `"}` }},
		{"event length", func(e *inboxJournalEvent) { e.EventID = strings.Repeat("x", 64*1024+1) }},
		{"unstorable time", func(e *inboxJournalEvent) { e.CreatedAt = "0000-01-01T00:00:00Z" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := inboxJournalEvent{EventID: "event", Project: "TEST", NodeID: "TEST-1", Author: "sender", Payload: `{"to":"worker","body":"mail"}`, CreatedAt: "2026-11-05T00:00:00Z"}
			tc.change(&e)
			j := &InboxJournal{Version: 1, Events: []inboxJournalEvent{e}}
			var err error
			j.Checksum, err = inboxJournalChecksum(j)
			require.NoError(t, err)
			require.Error(t, validateInboxJournal(j))
		})
	}
}

func TestInboxJournal_UnusualIdentity_JSONRoundTripPreservesBytes(t *testing.T) {
	for _, id := range []string{"odd id\nwith\x00control", "bad-utf8-\xff", strings.Repeat("x", 64*1024)} {
		t.Run(strings.ReplaceAll(id[:min(len(id), 8)], "\n", "_"), func(t *testing.T) {
			e := inboxJournalEvent{EventID: id, UID: id, Project: "DEP_ADD", NodeID: "DEP_ADD-1", Author: "sender", Payload: `{"to":"worker","body":"mail"}`, CreatedAt: "2026-11-05T00:00:00Z"}
			j := &InboxJournal{Version: 1, Events: []inboxJournalEvent{e}}
			var err error
			j.Checksum, err = inboxJournalChecksum(j)
			require.NoError(t, err)
			raw, err := json.Marshal(j)
			require.NoError(t, err)
			var restored InboxJournal
			require.NoError(t, json.Unmarshal(raw, &restored))
			require.Equal(t, id, restored.Events[0].EventID)
			require.Equal(t, id, restored.Events[0].UID)
			require.NoError(t, validateInboxJournal(&restored))
		})
	}
}
