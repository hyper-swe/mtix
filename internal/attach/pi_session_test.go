// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
)

func TestValidateSession_Table(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"uuid", "01a13d60-79cf-7000-8000-aaaaaaaaaaaa", false},
		{"colon and dot", "pi:abc.def_1", false},
		{"max length", strings.Repeat("a", attach.MaxSessionLen), false},
		{"empty", "", true},
		{"too long", strings.Repeat("a", attach.MaxSessionLen+1), true},
		{"leading dash looks like a flag", "-x", true},
		{"leading dot", ".x", true},
		{"space", "a b", true},
		{"shell metacharacters", "a;b", true},
		{"command substitution", "$(id)", true},
		{"newline", "a\nb", true},
		{"nul", "a\x00b", true},
		{"slash", "a/b", true},
		{"non-ascii", "sessión", true},
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
