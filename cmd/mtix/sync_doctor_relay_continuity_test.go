// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the doctor's relay continuity row and the exact RELAY_GAP fix text (MTIX-265).
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
)

// headerOnlySegment writes a segment holding just its header: an empty segment
// that ends where it starts.
func headerOnlySegment(t *testing.T, dir, peer string, no, firstRS uint64) {
	t.Helper()
	segDir := filepath.Join(dir, tick.PeersDirName, peer, tick.SegmentsDirName)
	require.NoError(t, os.MkdirAll(segDir, 0o700))
	h := segment.Header{FormatVersion: segment.FormatVersion, PeerID: peer, SegmentNo: no,
		FirstRS: firstRS, KeyEpoch: 1, PubEpoch: 1}
	raw, err := h.MarshalBinary()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(segDir, segment.FileName(no)), raw, 0o600))
}

func TestCheckRelayContinuity_MissingSegment_FailsWithExactFix(t *testing.T) {
	dir, doc := initDiagnosticRelay(t, false)
	peer := "1123456789abcdef"
	headerOnlySegment(t, dir, peer, 1, 1)
	headerOnlySegment(t, dir, peer, 3, 1)

	row := checkRelayContinuity(t.Context(), dir, doc)
	ok, detail := row.Pass, row.Detail
	require.False(t, ok)
	require.Equal(t, 1, strings.Count(detail, "RELAY_GAP:"), "the code appears once in the detail")
	require.Contains(t, detail, peer)
	require.Contains(t, detail, "segment 3 does not follow segment 1")
	require.Contains(t, detail, "fix: "+relayGapRecovery)
	require.Contains(t, detail, "does not clear a gap", "the fix never offers republish as the cure")
}

func TestCheckRelayContinuity_ContiguousAndOwnDirectory_Passes(t *testing.T) {
	dir, doc := initDiagnosticRelay(t, false)
	peer := "1123456789abcdef"
	headerOnlySegment(t, dir, peer, 1, 1)
	headerOnlySegment(t, dir, peer, 2, 1)
	headerOnlySegment(t, dir, diagnosticSelf, 5, 90) // own stream is never judged

	row := checkRelayContinuity(t.Context(), dir, doc)
	require.True(t, row.Pass, row.Detail)
	require.False(t, row.Warn, "a complete clean walk is a pass, not a warning")
}

func TestCheckRelayContinuity_RelaySequenceJumpBetweenConsecutiveSegments_Fails(t *testing.T) {
	dir, doc := initDiagnosticRelay(t, false)
	peer := "1123456789abcdef"
	headerOnlySegment(t, dir, peer, 1, 1)
	headerOnlySegment(t, dir, peer, 2, 5)

	row := checkRelayContinuity(t.Context(), dir, doc)
	ok, detail := row.Pass, row.Detail
	require.False(t, ok, "a jump in relay sequence is a gap even when segment numbers are consecutive")
	require.Contains(t, detail, "missing relay sequence 1 through 4")
}

func TestCheckRelayContinuity_UnreadablePeersDirectory_FailsRatherThanPassing(t *testing.T) {
	dir, doc := initDiagnosticRelay(t, false)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, tick.PeersDirName)))
	require.False(t, checkRelayContinuity(t.Context(), dir, doc).Pass)
}

func TestAppendRelayChecks_MissingSegment_ReportsContinuityRow(t *testing.T) {
	dir, _ := initDiagnosticRelay(t, true)
	peer := "1123456789abcdef"
	headerOnlySegment(t, dir, peer, 1, 1)
	headerOnlySegment(t, dir, peer, 4, 1)
	got := appendRelayChecks(t.Context(), DoctorReport{OverallPass: true})
	require.False(t, got.OverallPass)
	var found bool
	for _, c := range got.Checks {
		if c.Name == "relay continuity" {
			found = true
			require.False(t, c.Pass)
			require.Contains(t, c.Detail, "RELAY_GAP")
		}
	}
	require.True(t, found, "the continuity row is part of the relay checks")
}

func TestRelayStallRecovery_Gap_NeverOffersRepublishAsTheCure(t *testing.T) {
	got := relayStallRecovery("RELAY_GAP")
	require.Equal(t, relayGapRecovery, got)
	require.Contains(t, got, "restore the missing segment file")
	require.Contains(t, got, "new project")
	require.Contains(t, got, "already past the hole")
	require.Contains(t, got, "stalls again")
	require.Contains(t, got, "`mtix sync relay republish` does not clear a gap")
	require.NotContains(t, got, "republish --from")
}

func TestCheckRelayContinuity_UnreadableSegment_WarnsNotPasses(t *testing.T) {
	dir, doc := initDiagnosticRelay(t, false)
	peer := "1123456789abcdef"
	headerOnlySegment(t, dir, peer, 1, 1)
	headerOnlySegment(t, dir, peer, 2, 1)
	headerOnlySegment(t, dir, peer, 3, 1) // a gap behind the damaged segment
	segDir := filepath.Join(dir, tick.PeersDirName, peer, tick.SegmentsDirName)
	require.NoError(t, os.WriteFile(filepath.Join(segDir, segment.FileName(2)), []byte("garbage"), 0o600))

	row := checkRelayContinuity(t.Context(), dir, doc)
	require.True(t, row.Pass, "an unfinished walk does not fail the report")
	require.True(t, row.Warn, "and is not shown as a clean pass")
	require.Contains(t, row.Detail, "not fully checked")
	require.Contains(t, row.Detail, peer)
	require.Contains(t, row.Detail, "segment 2")
	require.Contains(t, row.Detail, "fix: "+relayContinuityUnchecked)
	require.Equal(t, relayContinuityUnchecked, row.Fix)
}

func TestCheckRelayContinuity_UnreadableKeys_WarnsWithFix(t *testing.T) {
	dir, doc := initDiagnosticRelay(t, true)
	require.NoError(t, os.RemoveAll(relayKeysDir()))
	row := checkRelayContinuity(t.Context(), dir, doc)
	require.True(t, row.Pass)
	require.True(t, row.Warn, "a walk that could not start is not a clean pass")
	require.NotEmpty(t, row.Fix)
	require.Contains(t, row.Detail, "not checked")
}

// TestCheckRelayContinuity_FailingRow_CarriesTheJSONFixAndTheUncheckedPeers: a
// failing row must be machine-readable like the warn row, and a peer the same
// walk could not check must not vanish because another peer has a hole.
func TestCheckRelayContinuity_FailingRow_CarriesTheJSONFixAndTheUncheckedPeers(t *testing.T) {
	dir, doc := initDiagnosticRelay(t, false)
	holed, unread := "1123456789abcdef", "2123456789abcdef"
	headerOnlySegment(t, dir, holed, 1, 1)
	headerOnlySegment(t, dir, holed, 3, 1)
	headerOnlySegment(t, dir, unread, 1, 1)
	segDir := filepath.Join(dir, tick.PeersDirName, unread, tick.SegmentsDirName)
	require.NoError(t, os.WriteFile(filepath.Join(segDir, segment.FileName(1)), []byte("garbage"), 0o600))
	headerOnlySegment(t, dir, unread, 2, 1)

	row := checkRelayContinuity(t.Context(), dir, doc)
	require.False(t, row.Pass)
	require.Equal(t, relayGapRecovery, row.Fix)
	require.Contains(t, row.Detail, "not fully checked")
	require.Contains(t, row.Detail, "peer "+unread)

	raw, err := json.Marshal(row)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, relayGapRecovery, decoded["fix"], "doctor --json carries the fix on the failing row")
}
