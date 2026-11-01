// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWakeGuidance_WordSplices_Consistent(t *testing.T) {
	pairs := []struct{ reader, control string }{
		{`$M in${U}box`, `$M da${U}te`},
		{`$M in$()box`, `$M da$()te`},
		{`$M in${U}$()box`, `$M da${U}$()te`},
		{`$M in""box`, `$M da""te`},
		{`$M in''box`, `$M da''te`},
		{`$M i'n'b"o"x`, `$M d'a't"e"`},
		{`$M i$'n'box`, `$M d$'a'te`},
		{`$M i$'\x6e'box`, `$M d$'\x61'te`},
		{`$M i$'\156'box`, `$M d$'\141'te`},
		{`$M in$''box`, `$M da$''te`},
	}
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, pair := range pairs {
		t.Run(pair.reader, func(t *testing.T) {
			assertWakeTokenPolicy(t, pair.reader, false)
			assertWakeTokenPolicy(t, pair.control, true)
			require.NotEmpty(t, wakeLaunchProblems(script+"\n"+pair.reader))
			require.Empty(t, wakeLaunchProblems(script+"\n"+pair.control))
		})
	}
}
