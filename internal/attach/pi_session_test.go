// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
)

// The wait verb and agent registration share one rule (model.ValidateSessionHandle).
func TestValidateSession_Table(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"uuid", "01a13d60-79cf-7000-8000-aaaaaaaaaaaa", false},
		{"colon and dot", "pi:abc.def_1", false},
		{"max length", strings.Repeat("a", attach.MaxSessionLen), false},
		{"socket style path", "/tmp/cc-socks/abc def.sock", false},
		{"non-ascii", "sessi\u00f3n", false},
		{"leading dash is data, argv only", "-x", false},
		{"empty", "", true},
		{"too long", strings.Repeat("a", attach.MaxSessionLen+1), true},
		{"newline", "a\nb", true},
		{"nul", "a\x00b", true},
		{"tab", "a\tb", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := attach.ValidateSession(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
