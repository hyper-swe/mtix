// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"github.com/stretchr/testify/require"
	"testing"
)

// FR-19.1 / MTIX-112.2.1: a correct checksum cannot opt into an unknown schema.
func TestValidateInboxJournal_UnsupportedVersionWithValidChecksum_RejectsInput(t *testing.T) {
	for _, version := range []int{0, 2} {
		j := &InboxJournal{Version: version, Events: []inboxJournalEvent{}}
		var err error
		j.Checksum, err = inboxJournalChecksum(j)
		require.NoError(t, err)
		require.ErrorContains(t, validateInboxJournal(j), "unsupported")
	}
}
