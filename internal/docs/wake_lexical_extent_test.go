// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWakeGuidance_LexicalExtent_Consistent(t *testing.T) {
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, pair := range [][2]string{
		{`$M i${U:+'$'}\nbox''`, `$M da${U:+'$'}\te''`},
		{`$M in${U:+'$'}\box''`, `$M da${U:+'$'}\te''`},
		{`$M i${U#'$'}\nbox''`, `$M da${U#'$'}\te''`},
		{`$M in$(: "$'")\box''`, `$M da$(: "$'")\te''`},
		{`$M $(: ]; echo in)box`, `$M $(: ]; echo da)te`},
		{`$M $(: }; echo in)box`, `$M $(: }; echo da)te`},
		{`$M $(case a in a) echo in;; esac)box`, `$M $(case a in a) echo da;; esac)te`},
		{`$M $'\551\556\542\557\570'`, `$M $'\544\541\564\545'`},
		{`$M $(case b in a) : esac;; b) echo in;; esac)box`, `$M $(case b in a) : esac;; b) echo da;; esac)te`},
		{`$M i$'\556'box`, `$M d$'\541'te`},
		{`$M i$'n\'b'ox`, `$M d$'a\'t'e`},
		{`$M "$(echo "in")"box`, `$M "$(echo "da")"te`},
		{`$M in\"box; true`, `$M da\"te; true`},
		{`$M @(in|in)box`, `$M @(da|da)te`},
		{`$M in$V1box`, `$M da$V1te`},
	} {
		t.Run(pair[0], func(t *testing.T) {
			assertWakeTokenPolicy(t, pair[0], false)
			assertWakeTokenPolicy(t, pair[1], true)
			require.NotEmpty(t, wakeLaunchProblems(script+"\n"+pair[0]))
			require.Empty(t, wakeLaunchProblems(script+"\n"+pair[1]))
		})
	}
}

func TestWakeGuidance_WordExtent(t *testing.T) {
	for _, word := range []string{
		`i$'n\';b'ox`, `d$'a\';t'e`,
		`"$(echo "in box")"`, `"$(echo "da te")"`,
		`in\";box`, `da\";te`,
		`@(in|in)box`, `@(da|da)te`,
	} {
		// An escaped quote is literal: the following unescaped semicolon separates commands.
		if word == `in\";box` || word == `da\";te` {
			require.Equal(t, []string{word[:4], word[5:]}, wakeSpecialWords(word))
		} else {
			require.Equal(t, []string{word}, wakeSpecialWords(word))
		}
	}
	require.Equal(t, []string{"inVbox"}, wakeWordSegments(`in$V1box`))
	require.Equal(t, []string{"daVte"}, wakeWordSegments(`da$V1te`))
}

func TestWakeGuidance_QuotedLiteralContext(t *testing.T) {
	for _, pair := range [][2]string{
		{`"$'\156'"`, `"$'\156'"`},
		{`'\156'`, `'\156'`},
		{`\$'\156'`, `\$'\156'`},
		{`"$(echo $'\151\156\142\157\170')"`, `"$(echo inbox)"`},
		{`$'\777'`, "\xff"},
		{`$'\400discarded'`, ""},
		{`$(`, `$(`}, {`$'`, `$'`}, {`"`, `"`},
	} {
		require.Equal(t, pair[1], wakeDecodeLiteralWord(pair[0]), pair[0])
	}
	for _, word := range []string{
		`$(echo case)box`, `$(case b in a) : esac;; b) echo in;; esac)box`,
		`$(case a in a) case b in b) echo in;; esac;; esac)box`,
	} {
		require.Equal(t, []string{word, "next"}, wakeSpecialWords(word+" next"))
	}
}
