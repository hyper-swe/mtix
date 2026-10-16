// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Pointer normalization preserves the first live instruction and all unrelated bytes.
package docs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendProjectPointer_Duplicates_PreservesFirstAndUnrelatedBytes(t *testing.T) {
	cases := []struct{ name, file, pointer, before, after string }{
		{"claude", "CLAUDE.md", "@MTIX.md", "user\n@MTIX.md\n\n@MTIX.md\ntail", "user\n@MTIX.md\n\ntail"},
		{"agents-alternate-first", "AGENTS.md", agentsPointer, "See MTIX.md for mtix instructions.\nuser\n" + agentsPointer + "\n", "See MTIX.md for mtix instructions.\nuser\n"},
		{"crlf", "CLAUDE.md", "@MTIX.md", "user\r\n@MTIX.md\r\n@MTIX.md\r\ntail\r\n", "user\r\n@MTIX.md\r\ntail\r\n"},
		{"bare-cr", "CLAUDE.md", "@MTIX.md", "user\r@MTIX.md\r@MTIX.md\rtail", "user\r@MTIX.md\rtail"},
		{"long-fence-short-close", "CLAUDE.md", "@MTIX.md", "~~~~\n@MTIX.md\n~~~\n@MTIX.md\n~~~~\n@MTIX.md\nuser\n@MTIX.md\n", "~~~~\n@MTIX.md\n~~~\n@MTIX.md\n~~~~\n@MTIX.md\nuser\n"},
		{"duplicate-no-newline", "CLAUDE.md", "@MTIX.md", "@MTIX.md\nuser\n@MTIX.md", "@MTIX.md\nuser\n"},
		{"first-indented", "CLAUDE.md", "@MTIX.md", "   @MTIX.md\nuser\n @MTIX.md\n", "   @MTIX.md\nuser\n"},
		{"fence-is-user-content", "CLAUDE.md", "@MTIX.md", "~~~\n@MTIX.md\n~~~\n@MTIX.md\nuser\n@MTIX.md\n", "~~~\n@MTIX.md\n~~~\n@MTIX.md\nuser\n"},
		{"deep-and-tab-examples", "CLAUDE.md", "@MTIX.md", "    @MTIX.md\n\t@MTIX.md\n@MTIX.md\nuser\n@MTIX.md\n", "    @MTIX.md\n\t@MTIX.md\n@MTIX.md\nuser\n"},
		{"unrelated-link", "AGENTS.md", agentsPointer, "Read [MTIX.md](custom.md) for mtix instructions.\n" + agentsPointer + "\nuser\n" + agentsPointer + "\n", "Read [MTIX.md](custom.md) for mtix instructions.\n" + agentsPointer + "\nuser\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := appendProjectPointer([]byte(tc.before), tc.pointer, tc.file)
			require.NoError(t, err)
			assert.Equal(t, []byte(tc.after), got)
		})
	}
}
