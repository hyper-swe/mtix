// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// TestVerdicts_CarryTheirNamedCode pins the operator-facing code on
// every FR-21 verdict this layer can return. Status, doctor, and the
// release-gate scenarios match on these strings, so they are contract.
func TestVerdicts_CarryTheirNamedCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{"symlink refusal", segment.ErrSymlink, "RELAY_SYMLINK"},
		{"foreign directory entry", segment.ErrForeignEntry, "RELAY_FOREIGN_ENTRY"},
		{"segment corrupt", segment.ErrSegmentCorrupt, "RELAY_SEGMENT_CORRUPT"},
		{"contiguity gap", segment.ErrGap, "RELAY_GAP"},
		{"peer id conflict", segment.ErrPeerIDConflict, "RELAY_PEER_ID_CONFLICT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.True(t, strings.HasPrefix(tt.err.Error(), tt.code+":"),
				"verdict %q must lead with its code", tt.err)
			require.Equal(t, tt.code, segment.CodeOf(tt.err))
			require.Equal(t, tt.code, segment.CodeOf(fmt.Errorf("wrapped: %w", tt.err)))
		})
	}
}

// TestCodeOf_UnclassifiedErrors returns empty rather than guessing.
func TestCodeOf_UnclassifiedErrors(t *testing.T) {
	require.Equal(t, "", segment.CodeOf(nil))
	require.Equal(t, "", segment.CodeOf(errors.New("something else")))
	require.Equal(t, "", segment.CodeOf(segment.ErrInvalidTransition))
}

// TestFrameVerdicts_ClassifyAsCorruption is the dispatch contract of
// FR-21 §5.4: every way a frame can fail to be valid data is a
// corruption-class verdict, so a caller reading a *sealed* segment can
// branch on one sentinel. The active-tail exemption is the reader's
// job (see the reader tests), not the parser's.
func TestFrameVerdicts_ClassifyAsCorruption(t *testing.T) {
	for _, err := range []error{
		segment.ErrBadMagic,
		segment.ErrPayloadTooLarge,
		segment.ErrCRCMismatch,
		segment.ErrMACMismatch,
		segment.ErrBadFormatVersion,
		segment.ErrBadPeerIDField,
	} {
		require.ErrorIs(t, err, segment.ErrSegmentCorrupt,
			"%v must dispatch as RELAY_SEGMENT_CORRUPT", err)
		require.Equal(t, "RELAY_SEGMENT_CORRUPT", segment.CodeOf(err))
	}
}

// TestErrIncomplete_IsNotCorruption is the load-bearing distinction of
// FR-21 §5.4: a short frame at the tail of the active segment is "not
// yet written", never damage. Conflating the two would turn every
// in-flight append into a fleet-wide stall.
func TestErrIncomplete_IsNotCorruption(t *testing.T) {
	require.False(t, errors.Is(segment.ErrIncomplete, segment.ErrSegmentCorrupt))
	require.True(t, segment.IsIncomplete(segment.ErrIncomplete))
	require.True(t, segment.IsIncomplete(fmt.Errorf("read tail: %w", segment.ErrIncomplete)))
	require.False(t, segment.IsIncomplete(segment.ErrCRCMismatch))
	require.False(t, segment.IsIncomplete(nil))
}

// TestGapVerdict_IsNotCorruption keeps a missing predecessor distinct
// from damaged bytes: the recoveries differ (republish vs re-bootstrap)
// and §5.4 forbids skipping either way.
func TestGapVerdict_IsNotCorruption(t *testing.T) {
	require.False(t, errors.Is(segment.ErrGap, segment.ErrSegmentCorrupt))
	require.False(t, errors.Is(segment.ErrSegmentCorrupt, segment.ErrGap))
}
