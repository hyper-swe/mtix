// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

const testPeerID = "0123456789abcdef"

// TestValidatePeerID enforces the FR-21 §5.2 grammar
// ^[0-9a-f]{16}(-[a-z0-9_-]{1,32})?$ — the machine-hash prefix with an
// optional operator label. Everything else is a foreign entry.
func TestValidatePeerID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"bare machine hash", "0123456789abcdef", false},
		{"all hex digits", "ffffffffffffffff", false},
		{"labelled", "0123456789abcdef-laptop", false},
		{"label with digits", "0123456789abcdef-seat2", false},
		{"label with underscore and dash", "0123456789abcdef-a_b-c", false},
		{"label at max length", "0123456789abcdef-" + strings.Repeat("a", 32), false},
		{"empty", "", true},
		{"too short", "0123456789abcde", true},
		{"too long", "0123456789abcdef0", true},
		{"uppercase hex", "0123456789ABCDEF", true},
		{"non hex character", "0123456789abcdeg", true},
		{"empty label", "0123456789abcdef-", true},
		{"label too long", "0123456789abcdef-" + strings.Repeat("a", 33), true},
		{"uppercase label", "0123456789abcdef-Laptop", true},
		{"label with dot", "0123456789abcdef-a.b", true},
		{"path traversal", "../0123456789abcdef", true},
		{"separator in id", "0123456789abcdef/x", true},
		{"two labels", "0123456789abcdef-a-b", false},
		{"leading dash label", "0123456789abcdef--a", false},
		{"nul byte", "0123456789abcde\x00", true},
		{"trailing space", "0123456789abcdef ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := segment.ValidatePeerID(tt.id)
			if tt.wantErr {
				require.ErrorIs(t, err, segment.ErrForeignEntry)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestSegmentFileName renders the FR-21 §5.2 zero-padded name.
func TestSegmentFileName(t *testing.T) {
	tests := []struct {
		name string
		no   uint64
		want string
	}{
		{"first segment", 1, "seg-00000001.mrseg"},
		{"two digits", 42, "seg-00000042.mrseg"},
		{"eight digits", 12345678, "seg-12345678.mrseg"},
		{"beyond eight digits keeps every digit", 123456789, "seg-123456789.mrseg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, segment.FileName(tt.no))
		})
	}
}

// TestParseFileName round-trips the name back to its ordering number
// and refuses everything else. Naming carries ordering (§5.2), so a
// sloppy parse here would reorder a peer's stream.
func TestParseFileName(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		want    uint64
		wantErr bool
	}{
		{"first segment", "seg-00000001.mrseg", 1, false},
		{"padded", "seg-00000042.mrseg", 42, false},
		{"wide", "seg-123456789.mrseg", 123456789, false},
		{"zero is not a segment number", "seg-00000000.mrseg", 0, true},
		{"wrong extension", "seg-00000001.dat", 0, true},
		{"no extension", "seg-00000001", 0, true},
		{"wrong prefix", "segment-00000001.mrseg", 0, true},
		{"under padded", "seg-1.mrseg", 0, true},
		{"non numeric", "seg-0000000x.mrseg", 0, true},
		{"negative", "seg--0000001.mrseg", 0, true},
		{"empty", "", 0, true},
		{"ack file", "acks.json", 0, true},
		{"relay metadata", "relay.json", 0, true},
		{"temp file", "seg-00000001.mrseg.tmp", 0, true},
		{"overflow", "seg-99999999999999999999999.mrseg", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := segment.ParseFileName(tt.file)
			if tt.wantErr {
				require.ErrorIs(t, err, segment.ErrForeignEntry)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestFileName_ParseFileName_RoundTrip pins the pair together.
func TestFileName_ParseFileName_RoundTrip(t *testing.T) {
	for _, no := range []uint64{1, 2, 99, 100000000, 4294967296} {
		got, err := segment.ParseFileName(segment.FileName(no))
		require.NoError(t, err)
		require.Equal(t, no, got)
	}
}
