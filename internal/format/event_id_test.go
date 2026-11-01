// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventID_VariedIDs_RendersPrintableASCIIAndRoundTrips(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string // exact rendering where it is stable; empty to skip
	}{
		{"uuid v7", "01890a5d-ac96-774b-bcce-b302099a8057", `"01890a5d-ac96-774b-bcce-b302099a8057"`},
		{"uuid v8", "320c3d4d-cc00-875b-8ec9-32d5f69181c0", `"320c3d4d-cc00-875b-8ec9-32d5f69181c0"`},
		{"control characters", "ev\x01\x1b[31m\r\n\t\x7fend", `"ev\x01\x1b[31m\r\n\t\x7fend"`},
		{"nul", "a\x00b", `"a\x00b"`},
		{"non-ascii", "\u00e9v\u00e9nement-\u65e5\u672c\u8a9e-\u202e", `"\u00e9v\u00e9nement-\u65e5\u672c\u8a9e-\u202e"`},
		{"invalid utf-8", "a\xffb", `"a\xffb"`},
		{"empty", "", `""`},
		{"very long", strings.Repeat("é\x00x", 50000), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EventID(tt.id)
			for i := 0; i < len(got); i++ {
				require.GreaterOrEqual(t, got[i], byte(0x20), "byte %d below 0x20", i)
				require.LessOrEqual(t, got[i], byte(0x7e), "byte %d above 0x7e", i)
			}
			back, err := strconv.Unquote(got)
			require.NoError(t, err)
			assert.Equal(t, tt.id, back, "round trip must return the exact original")
			if tt.want != "" {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
