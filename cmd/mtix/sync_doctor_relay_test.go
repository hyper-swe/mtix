// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// TestRelayVerdictRegistry_MatchesTheSpecExactly is the FR-21 §9
// contract: status and doctor assert against exactly the pinned list.
//
// Both directions are checked, and both catch a real drift. A code the
// packages can produce but the registry does not name would reach an
// operator with no documented meaning. A code the registry names but
// nothing can produce is a check nobody wrote — the more dangerous
// half, because the list reads as covered.
func TestRelayVerdictRegistry_MatchesTheSpecExactly(t *testing.T) {
	// Every code the relay packages actually export, gathered from the
	// packages that own them rather than restated here.
	owned := map[string]bool{
		segment.CodeSegmentCorrupt:      true,
		segment.CodeGap:                 true,
		segment.CodeSymlink:             true,
		segment.CodeForeignEntry:        true,
		segment.CodePeerIDConflict:      true,
		keyring.CodeKeyAbsent:           true,
		keyring.CodeKeyPerms:            true,
		keyring.CodeKeyInvalid:          true,
		keyring.CodeKeySymlink:          true,
		metadata.CodeMetaAbsent:         true,
		metadata.CodeMetaCorrupt:        true,
		metadata.CodeMetaSymlink:        true,
		lifecycle.CodeModeMismatch:      true,
		lifecycle.CodeHistoryDiverged:   true,
		lifecycle.CodeNoSharedProject:   true,
		publisher.CodePublisherDiverged: true,
		ingest.CodeAuthFail:             true,
	}

	pinned := map[string]bool{}
	for _, code := range relayVerdictCodes {
		pinned[code] = true
	}

	for code := range owned {
		require.Truef(t, pinned[code],
			"%s is produced by the relay packages but is not in the §9 registry — "+
				"an operator would meet a code with no documented meaning", code)
	}
	for code := range pinned {
		require.Truef(t, owned[code],
			"%s is in the §9 registry but no relay package produces it — "+
				"a check nobody wrote, in a list that reads as covered", code)
	}
	require.Len(t, relayVerdictCodes, len(owned), "no duplicates in the registry")
}

// TestRelayCodeOf_ResolvesEveryOwningPackage keeps doctor's single
// reconciliation point honest. The codes live in six packages; if this
// resolver misses one, that verdict reaches an operator without the
// recovery command attached.
func TestRelayCodeOf_ResolvesEveryOwningPackage(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{segment.ErrSegmentCorrupt, segment.CodeSegmentCorrupt},
		{segment.ErrGap, segment.CodeGap},
		{segment.ErrSymlink, segment.CodeSymlink},
		{segment.ErrForeignEntry, segment.CodeForeignEntry},
		{segment.ErrPeerIDConflict, segment.CodePeerIDConflict},
		{keyring.ErrKeyAbsent, keyring.CodeKeyAbsent},
		{keyring.ErrKeyPerms, keyring.CodeKeyPerms},
		{keyring.ErrKeyInvalid, keyring.CodeKeyInvalid},
		{keyring.ErrKeySymlink, keyring.CodeKeySymlink},
		{metadata.ErrRelayAbsent, metadata.CodeMetaAbsent},
		{metadata.ErrRelayCorrupt, metadata.CodeMetaCorrupt},
		{metadata.ErrRelaySymlink, metadata.CodeMetaSymlink},
		{lifecycle.ErrModeMismatch, lifecycle.CodeModeMismatch},
		{lifecycle.ErrHistoryDiverged, lifecycle.CodeHistoryDiverged},
		{lifecycle.ErrNoSharedProject, lifecycle.CodeNoSharedProject},
		{publisher.ErrPublisherDiverged, publisher.CodePublisherDiverged},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			require.Equal(t, tt.want, relayCodeOf(tt.err))
			require.Equal(t, tt.want, relayCodeOf(fmt.Errorf("wrapped: %w", tt.err)),
				"a wrapped verdict must still resolve")
		})
	}

	t.Run("an unclassified error resolves to nothing", func(t *testing.T) {
		require.Equal(t, "", relayCodeOf(errors.New("disk on fire")))
		require.Equal(t, "", relayCodeOf(nil))
	})
}

// TestRelayStallRecovery_NamesACommandForEveryVerdict is the §9 house
// rule enforced rather than trusted: an error that does not name the
// next command is a bug, so every code in the registry must produce
// guidance, and the fallback must not be empty either.
func TestRelayStallRecovery_NamesACommandForEveryVerdict(t *testing.T) {
	for _, code := range relayVerdictCodes {
		t.Run(code, func(t *testing.T) {
			require.NotEmpty(t, relayStallRecovery(code),
				"%s reaches an operator with no next step", code)
		})
	}
	require.NotEmpty(t, relayStallRecovery(""),
		"even an unclassified failure must say what to try")
}
