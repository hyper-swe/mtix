// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWakeGuidance_SpecialWordClass_Consistent(t *testing.T) {
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, pair := range wakeSpecialWordPairs() {
		t.Run(pair[0], func(t *testing.T) {
			assertWakeTokenPolicy(t, pair[0], false)
			assertWakeTokenPolicy(t, pair[1], true)
			require.NotEmpty(t, wakeLaunchProblems(script+"\n"+pair[0]))
			require.Empty(t, wakeLaunchProblems(script+"\n"+pair[1]))
		})
	}
	for _, control := range []string{"echo i n b o x", "$I n b o x", "$I;n;b;o;x", "$I|n|b|o|x", "$M xobni", "echo $xobni", "echo input_box", "echo INBOX", "$M input_count", "$M --help", "$TOOL run", "$M ./docs/input",
		`printf ' re-resolve via mtix and use the settled (fully-numeric) id before externalizing.\n' >&2`,
		`echo 'This long quoted sentence includes several words before another example.'`,
		`$M 'i n b o x'`, `$M i\ n\ b\ o\ x`, `$M i$'n\' b o x'`,
		"$M 'i\tnbox'", `$M i$'n\x20box'`,
		`fetch i@nbox`, `fetch i~nbox`, `echo 'input_box'`,
		`export MTIX_SYNC_DSN="postgresql://mtix_sync@hub.example.com:5432/mtix_hub?sslmode=verify-full"`,
	} {
		assertWakeTokenPolicy(t, control, true)
	}
}

func wakeSpecialWordPairs() [][2]string {
	return [][2]string{
		{`$M "in"box`, `$M "da"te`},
		{`$M in$'\0'box`, `$M da$'\0'te`},
		{`$M in$'\c@'box`, `$M da$'\c@'te`},
		{`$M in$'\x00'box`, `$M da$'\x00'te`},
		{`$M in$'\u0'box`, `$M da$'\u0'te`},
		{`$M in$'\0discarded text'box`, `$M da$'\0discarded text'te`},
		{`$M IN${U}BOX`, `$M DA${U}TE`},
		{`fetch i[n]box`, `fetch d[a]te`},
		{`fetch i*nbox`, `fetch d*ate`},
		{`fetch i?nbox`, `fetch d?ate`},
		{`$M in$U'box'`, `$M da$U'te'`},
		{`$M in$U\box`, `$M da$U\te`},
		{"$M in``box", "$M da``te"},
		{`$M in$(:)box`, `$M da$(:)te`},
		{`$M in$(true)box`, `$M da$(true)te`},
		{`$M in$( )box`, `$M da$( )te`},
		{`$M in${U:-${V}}box`, `$M d${U:-${V}}ate`},
		{`$M ${X:-in}box`, `$M ${X:-da}te`},
		{`$M in$@box`, `$M da$@te`},
		{`$M in$*box`, `$M da$*te`},
		{`$M in$1box`, `$M da$1te`},
		{`$M in$#box`, `$M da$#te`},
		{`$M in$"b"ox`, `$M da$"t"e`},
		{`$M i$'\u6e'box`, `$M d$'\u61'te`},
		{`$M i$'\U6e'box`, `$M d$'\U61'te`},
		{"$M in\\\nbox", "$M da\\\nte"},
		{`$M {in,}box`, `$M {da,}te`},
		{`$M i{n,}box`, `$M d{a,}te`},
		{`$M in{,}box`, `$M da{,}te`},
		{`$M in{b..b}ox`, `$M da{t..t}e`},
		{`$M i${U}n${V}box`, `$M d${U}a${V}te`},
		{`$M in"${U}"box`, `$M da"${U}"te`},
		{`$M in${U:-}box`, `$M da${U:-}te`},
		{`$M in$'\q'box`, `$M da$'\q'te`},
		{`$M in$((0))box`, `$M da$((0))te`},
		{`echo $input_box`, `echo $input_count`},
		{`$input_box --help`, `$input_count --help`},
		{`cat docs/inbox.md`, `cat docs/input.md`},
		{`$M in$(true ' ')box`, `$M da$(true ' ')te`},
		{`$M $'\u0069\u006e\u0062\u006f\u0078'`, `$M $'\u0064\u0061\u0074\u0065'`},
		{`$M \U00000069\U0000006e\U00000062\U0000006f\U00000078`, `$M \U00000064\U00000061\U00000074\U00000065`},
		{`$M \x69\x6e\x62\x6f\x78`, `$M \x64\x61\x74\x65`},
		{`$M \151\156\142\157\170`, `$M \144\141\164\145`},
	}
}

func TestWakeGuidance_LiteralEscapeDecoding(t *testing.T) {
	for _, pair := range [][2]string{
		{`in$U\box`, `in$U\box`},
		{`$'a\0b'`, "a"},
		{`$'\c@discarded'`, ""},
		{`$'\cA\c?\E'`, "\x01\x7f\x1b"},
		{`$'\0suffix'`, ""},
		{`i$'\x6e'box`, "inbox"},
		{`i$'\u6e'box`, "inbox"},
		{`i$'\U0000006e'box`, "inbox"},
		{`i$'\156'box`, "inbox"},
		{`$'\b\n\t'`, "\b\n\t"},
		{`$'\q'`, `\q`},
		{`$'\\x6e'`, `\x6e`},
	} {
		require.Equal(t, pair[1], wakeDecodeLiteralWord(pair[0]))
	}
}

func TestWakeGuidance_LiteralSegments_StaySeparate(t *testing.T) {
	for _, literal := range []string{"/", ":", ".", "@", "=", "1", "_", "+", "-", "~"} {
		assertWakeTokenPolicy(t, `$M i"`+literal+`"nbox`, true)
	}
	for _, pair := range [][2]string{
		{`$M in${U/x/y}box`, `$M da${U/x/y}te`},
		{`$M in[b-b]ox`, `$M da[t-t]e`},
		{`sh -c '$M in${U/x/y}box'`, `sh -c '$M da${U/x/y}te'`},
	} {
		assertWakeTokenPolicy(t, pair[0], false)
		assertWakeTokenPolicy(t, pair[1], true)
	}
}
