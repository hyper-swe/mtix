// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// FR-19.1: independently checksummed invalid headers must fail the page bounds,
// rather than being rejected incidentally by the checksum first.
func TestRecoverWAL_CraftedPageBounds(t *testing.T) {
	for _, tc := range []struct{ name, hex string }{
		{"too-small", "377f0682002de2180000010000000000112233445566778818fc2524594c631e"},
		{"not-power-two", "377f0682002de218000002010000000011223344556677881afe25245c4f631e"},
		{"too-large", "377f0682002de2180002000000000000112233445566778818fa29245949691e"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := hex.DecodeString(tc.hex)
			require.NoError(t, err)
			require.ErrorContains(t, verifyRecoverWAL(t.Context(), bytes.NewReader(data), int64(len(data))), "page size")
		})
	}
}

// FR-19.1: a fully checksummed frame still needs a nonzero page number.
func TestRecoverWAL_CraftedZeroPage(t *testing.T) {
	data, err := hex.DecodeString("377f0682002de2180000020000000000112233445566778818fe2524594f631e000000000000000111223344556677886e88905aa8e4398a")
	require.NoError(t, err)
	data = append(data, make([]byte, 512)...)
	require.ErrorContains(t, verifyRecoverWAL(t.Context(), bytes.NewReader(data), int64(len(data))), "no page number")
}
