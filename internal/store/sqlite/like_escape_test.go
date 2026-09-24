// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEscapeLIKEPrefix_Metacharacters_EscapedLiterally pins the escaping that
// every subtree LIKE pattern relies on (MTIX-33, MTIX-95.17): each LIKE
// metacharacter (\, %, _) gains a backslash so it matches literally under
// `ESCAPE '\'`, and every other character passes through unchanged.
func TestEscapeLIKEPrefix_Metacharacters_EscapedLiterally(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain id unchanged", "PROJ-1.2", "PROJ-1.2"},
		{"empty", "", ""},
		{"underscore", "A_B-1", `A\_B-1`},
		{"percent", "A%B-1", `A\%B-1`},
		{"backslash", `A\B-1`, `A\\B-1`},
		{"every metacharacter", `_%\`, `\_\%\\`},
		{"repeated underscores", "A__B-1.1", `A\_\_B-1.1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, escapeLIKEPrefix(tt.in))
		})
	}
}
