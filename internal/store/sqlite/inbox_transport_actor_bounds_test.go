// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// FR-19.1 / MTIX-112.2.1: transport bounds identities without normalizing them.
func TestValidateInboxJournal_BoundedActors_PreservesExactValues(t *testing.T) {
	for _, actor := range []string{"Opus", "agent.one", "Opus Reviewer", "codex/lane-1", "worker\n", "work\u200ber", "   ", strings.Repeat("x", 64)} {
		t.Run(actor, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{"to": actor, "author_id": actor, "body": "mail"})
			require.NoError(t, err)
			e := inboxJournalEvent{EventID: "event", Project: "TEST", NodeID: "TEST-1", Author: actor, Payload: string(raw), CreatedAt: "2026-11-05T00:00:00Z"}
			require.NoError(t, validateInboxEvent(e))
			j := &InboxJournal{Version: 1, Events: []inboxJournalEvent{e}}
			j.Checksum, err = inboxJournalChecksum(j)
			require.NoError(t, err)
			encoded, err := json.Marshal(j)
			require.NoError(t, err)
			var decoded InboxJournal
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			require.NoError(t, validateInboxJournal(&decoded))
			require.Equal(t, e, decoded.Events[0])
		})
	}
}

func TestValidateInboxEvent_RequiredAndOptionalActorBounds_RejectsOnlyOutOfBounds(t *testing.T) {
	for _, tc := range []struct {
		name, author, recipient, payloadAuthor string
		valid                                  bool
	}{
		{"empty envelope", "", "worker", "", false},
		{"long envelope", strings.Repeat("x", 65), "worker", "", false},
		{"empty recipient", "sender", "", "", false},
		{"long recipient", "sender", strings.Repeat("x", 65), "", false},
		{"long payload author", "sender", "worker", strings.Repeat("x", 65), false},
		{"absent payload author", "sender", "worker", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]string{"to": tc.recipient, "body": "mail"}
			if tc.payloadAuthor != "" {
				payload["author_id"] = tc.payloadAuthor
			}
			raw, err := json.Marshal(payload)
			require.NoError(t, err)
			e := inboxJournalEvent{EventID: "event", Project: "TEST", NodeID: "TEST-1", Author: tc.author, Payload: string(raw), CreatedAt: "2026-11-05T00:00:00Z"}
			err = validateInboxEvent(e)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
