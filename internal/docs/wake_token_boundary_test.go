// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Token boundaries have the same policy in every executable context.
func TestWakeGuidance_TokenBoundaries_Consistent(t *testing.T) {
	pairs := []struct{ reader, control string }{
		{`{$M,inbox}`, `{$M,date}`},
		{`{mtix,inbox}`, `{mtix,help}`},
		{`$M {inbox,}`, `$M {input,}`},
		{`$M {,inbox}`, `$M {,input}`},
		{`$M ${X:-inbox}`, `$M ${X:-date}`},
		{`$M ${1:-inbox}`, `$M ${1:-date}`},
		{`SUB=inbox; $M $SUB`, `SUB=input; $M $SUB`},
		{`date; SUB=inbox`, `date; SUB=input`},
		{`$M --inbox`, `$M --help`},
		{`$M ${X:+inbox}`, `$M ${X:+date}`},
		{`$M ${X+inbox}`, `$M ${X+date}`},
		{`$M ${X/x/inbox}`, `$M ${X/x/date}`},
		{`$M ${X//*/inbox}`, `$M ${X//*/date}`},
		{`IFS=.; $M $(echo inbox.)`, `IFS=.; $M $(echo date.)`},
		{`IFS=+; V=inbox+; $M $V`, `IFS=+; V=date+; $M $V`},
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

func TestWakeGuidance_TokenBoundaryPositions_Consistent(t *testing.T) {
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, separator := range []string{",", "=", ":", "-", "+", "/", ".", "@", "%", "~", "^", "#", "*", "?", "[", "]"} {
		for _, position := range []struct{ left, right string }{{separator, ""}, {"", separator}, {"left" + separator, separator + "right"}} {
			text := "$M " + position.left + "inbox" + position.right
			t.Run(text, func(t *testing.T) {
				assertWakeTokenPolicy(t, text, false)
				require.NotEmpty(t, wakeLaunchProblems(script+"\n"+text))
			})
		}
	}
	for _, text := range []string{"date -u", "$M --help", "$TOOL run", "SUB=input", "$M input_count", "./docs/input"} {
		assertWakeTokenPolicy(t, text, true)
		require.Empty(t, wakeLaunchProblems(script+"\n"+text))
	}
}
