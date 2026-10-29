// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestAgentText_InvisibleRunes_Neutralized checks every rune in the hostile
// ranges, including emoji byte smuggling, through the briefing sanitizer, the
// briefing renderer and the inbox envelope, so both agent-facing surfaces
// share one rule (MTIX-100.8).
func TestAgentText_InvisibleRunes_Neutralized(t *testing.T) {
	ranges := [][2]rune{{0xfe00, 0xfe0f}, {0xe0100, 0xe01ef}, {0x2060, 0x2064}, {0x2066, 0x206f},
		{0xfff9, 0xfffb}, {0x180e, 0x180e}, {0x034f, 0x034f}, {0x115f, 0x1160},
		{0x3164, 0x3164}, {0xffa0, 0xffa0}, {0x061c, 0x061c}, {0x200e, 0x200f},
		{0x00ad, 0x00ad}, {0x2028, 0x2029}, {0xe0020, 0xe007f}}
	for _, span := range ranges {
		for r := span[0]; r <= span[1]; r++ {
			t.Run(fmt.Sprintf("U+%04X", r), func(t *testing.T) {
				in := "🙂" + string(r) + "FAIL"
				if r == 0xfe0f {
					in = "x" + string(r) + "FAIL" // after a symbol FE0F is kept: see the emoji table
				}
				want := "🙂�FAIL"
				if r == 0xfe0f {
					want = "x�FAIL"
				}
				assert.Equal(t, want, sanitizeControlChars(in))
				var briefing strings.Builder
				require.NoError(t, RenderBriefing(&briefing, []*model.Node{{ID: "TEST-1", Title: in}}, BriefingOpts{}))
				assert.Contains(t, briefing.String(), want)
				text, err := RenderEnvelope("worker", []Event{{Seq: 1, Task: "TEST-1", Sender: "sender", Body: in}}, Caps{})
				require.NoError(t, err)
				wantDrop := "🙂FAIL"
				if r == 0xfe0f {
					wantDrop = "xFAIL"
				}
				assert.Equal(t, wantDrop, neutralize(in))
				assert.Contains(t, text, "body: "+strconv.QuoteToASCII(wantDrop))
			})
		}
	}
}

// agentRuneCases are the boundaries of every class the agent rule removes
// (start, end and the neighbours on each side) and the visible runes next to
// them that must stay. A neighbour is chosen only when it is an assigned,
// visible character, so this table never pins an unsafe rune as kept.
var agentRuneCases = []struct {
	name   string
	r      rune
	unsafe bool
}{
	// Variation selectors: Basic Multilingual Plane block.
	{"VS1 start FE00", 0xfe00, true}, {"VS16 end FE0F", 0xfe0f, true}, {"after VS block FE10", 0xfe10, false},
	// Variation selectors supplement.
	{"VS17 start E0100", 0xe0100, true}, {"VS256 end E01EF", 0xe01ef, true},
	// Mongolian free variation selectors and the Mongolian vowel separator.
	{"Mongolian before 180B (180A)", 0x180a, false}, {"MFVS1 180B", 0x180b, true}, {"MFVS2 180C", 0x180c, true},
	{"MFVS3 180D", 0x180d, true}, {"vowel separator 180E", 0x180e, true}, {"MFVS4 180F", 0x180f, true},
	{"Mongolian digit zero 1810", 0x1810, false},
	// Khmer inherent vowels.
	{"Khmer before 17B4 (17B3)", 0x17b3, false}, {"Khmer inherent aq 17B4", 0x17b4, true},
	{"Khmer inherent aa 17B5", 0x17b5, true}, {"Khmer vowel sign aa 17B6", 0x17b6, false},
	// Combining grapheme joiner and Hangul fillers with neighbours.
	{"before CGJ 034E", 0x034e, false}, {"CGJ 034F", 0x034f, true}, {"after CGJ 0350", 0x0350, false},
	{"before choseong filler 115E", 0x115e, false}, {"choseong filler 115F", 0x115f, true},
	{"jungseong filler 1160", 0x1160, true}, {"after jungseong filler 1161", 0x1161, false},
	{"before 3164 (3163)", 0x3163, false}, {"filler 3164", 0x3164, true}, {"after 3164 (3165)", 0x3165, false},
	{"before FFA0 (FF9F)", 0xff9f, false}, {"filler FFA0", 0xffa0, true}, {"after FFA0 (FFA1)", 0xffa1, false},
	// Format characters (Cf) and their neighbours.
	{"soft hyphen 00AD", 0x00ad, true}, {"before soft hyphen (00AC)", 0x00ac, false}, {"after soft hyphen (00AE)", 0x00ae, false},
	{"word joiner 2060", 0x2060, true}, {"invisible plus 2064", 0x2064, true}, {"2070 superscript zero", 0x2070, false},
	{"isolate 2066", 0x2066, true}, {"deprecated format 206F", 0x206f, true},
	{"interlinear FFF9", 0xfff9, true}, {"interlinear FFFB", 0xfffb, true},
	{"object replacement FFFC", 0xfffc, false}, {"replacement FFFD", 0xfffd, false},
	{"directional mark 200E", 0x200e, true}, {"directional mark 200F", 0x200f, true}, {"arabic letter mark 061C", 0x061c, true},
	// Unassigned default-ignorable runes and gaps.
	{"unassigned 2065", 0x2065, true},
	{"unassigned FFF0", 0xfff0, true}, {"unassigned FFF8", 0xfff8, true},
	{"tag block start E0000", 0xe0000, true}, {"unassigned E0002", 0xe0002, true}, {"unassigned E001F", 0xe001f, true},
	{"tag E0020", 0xe0020, true}, {"tag E007F", 0xe007f, true},
	{"unassigned E0080", 0xe0080, true}, {"unassigned E00FF", 0xe00ff, true},
	{"unassigned E01F0", 0xe01f0, true}, {"unassigned E0FFF", 0xe0fff, true},
	// Noncharacters.
	{"noncharacter FDD0", 0xfdd0, true}, {"noncharacter FDEF", 0xfdef, true},
	{"after the noncharacters (FDF0)", 0xfdf0, false}, {"ligature FDFD", 0xfdfd, false},
	{"noncharacter FFFE", 0xfffe, true}, {"noncharacter FFFF", 0xffff, true},
	{"noncharacter 1FFFE", 0x1fffe, true}, {"noncharacter 1FFFF", 0x1ffff, true},
	{"noncharacter 10FFFE", 0x10fffe, true}, {"noncharacter 10FFFF", 0x10ffff, true},
	// Visible text that must stay.
	{"emoji", 0x1f642, false}, {"e acute", 0xe9, false}, {"CJK", 0x4e2d, false}, {"Arabic", 0x0639, false},
	{"no-break space", 0xa0, false}, {"ideographic space", 0x3000, false},
	{"private use (kept, not an invisible payload class)", 0xe000, false},
}

// TestAgentText_ClassBoundaries_UnsafeOrKept drives the shared rule through
// every entry point for the start, end and neighbours of each class.
func TestAgentText_ClassBoundaries_UnsafeOrKept(t *testing.T) {
	for _, tc := range agentRuneCases {
		t.Run(fmt.Sprintf("%s U+%04X", tc.name, tc.r), func(t *testing.T) {
			in := "🙂" + string(tc.r) + "ok"
			prefix := "🙂"
			if tc.r == 0xfe0f {
				in, prefix = "x"+string(tc.r)+"ok", "x" // FE0F after a symbol is kept: see the emoji table
			}
			assert.Equal(t, tc.unsafe, isAgentUnsafe(tc.r))
			if tc.unsafe {
				assert.Equal(t, prefix+"�ok", sanitizeControlChars(in))
				assert.Equal(t, prefix+"ok", neutralize(in))
				return
			}
			assert.Equal(t, in, sanitizeControlChars(in))
			assert.Equal(t, in, neutralize(in))
		})
	}
}

// TestAgentText_EmojiPresentationSelector_KeptOnlyDirectlyAfterSymbol pins the
// one exception to the variation-selector rule: exactly one U+FE0F directly
// after a symbol rune (category So, which covers emoji bases) is kept, so
// ordinary emoji render. Any other U+FE0F, a repeated one, a run of selectors
// and U+FE0E are still unsafe.
func TestAgentText_EmojiPresentationSelector_KeptOnlyDirectlyAfterSymbol(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantSan  string
		wantDrop string
	}{
		{"emoji then FE0F kept", "🙂\ufe0f", "🙂\ufe0f", "🙂\ufe0f"},
		{"heart symbol then FE0F kept", "\u2764\ufe0f", "\u2764\ufe0f", "\u2764\ufe0f"},
		{"letter then FE0F unsafe", "a\ufe0f", "a\uFFFD", "a"},
		{"digit then FE0F unsafe", "1\ufe0f", "1\uFFFD", "1"},
		{"emoji then FE0F FE0F, second unsafe", "🙂\ufe0f\ufe0f", "🙂\ufe0f\uFFFD", "🙂\ufe0f"},
		{"emoji then FE0E unsafe", "🙂\ufe0e", "🙂\uFFFD", "🙂"},
		{"emoji then FE0F then FE0E, second unsafe", "🙂\ufe0f\ufe0e", "🙂\ufe0f\uFFFD", "🙂\ufe0f"},
		{"emoji then other selector unsafe", "🙂\ufe00", "🙂\uFFFD", "🙂"},
		{"emoji then supplement selector unsafe", "🙂\U000e0100", "🙂\uFFFD", "🙂"},
		{"zero-width joiner between unsafe", "🙂\u200d\ufe0f", "🙂\uFFFD\uFFFD", "🙂"},
		{"FE0F at start unsafe", "\ufe0f", "\uFFFD", ""},
		{"math symbol Sm then FE0F unsafe", "+\ufe0f", "+\uFFFD", "+"},
		{"currency symbol Sc then FE0F unsafe", "$\ufe0f", "$\uFFFD", "$"},
		{"modifier symbol Sk then FE0F unsafe", "^\ufe0f", "^\uFFFD", "^"},
		{"braille blank then FE0F unsafe", "\u2800\ufe0f", "\u2800\uFFFD", "\u2800"},
		{"replacement character then FE0F unsafe", "\uFFFD\ufe0f", "\uFFFD\uFFFD", "\uFFFD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantSan, sanitizeControlChars(tt.in))
			assert.Equal(t, tt.wantDrop, neutralize(tt.in))
		})
	}
}

// TestAgentText_BrailleBlank_Kept records that U+2800 is deliberately kept: it
// is an assigned symbol with visible width, not an unassigned or format rune.
func TestAgentText_BrailleBlank_Kept(t *testing.T) {
	assert.Equal(t, "a\u2800b", sanitizeControlChars("a\u2800b"))
	assert.Equal(t, "a\u2800b", neutralize("a\u2800b"))
}

// TestAgentText_PlainText_Preserved keeps whitespace, tab and newline and
// multilingual text intact.
func TestAgentText_PlainText_Preserved(t *testing.T) {
	for _, in := range []string{"", "a\tb\nc", "é中文 العربية", "🙂", "\u00a0\u3000"} {
		assert.Equal(t, in, sanitizeControlChars(in))
		assert.Equal(t, in, neutralize(in))
	}
}

// TestAgentText_HumanDisplayRule_KeepsDirectionalMarksAndSelectors pins that
// the agent rule does not change what a person sees: the show-path strippers
// keep the directional marks and the variation selector after an emoji.
func TestAgentText_HumanDisplayRule_KeepsDirectionalMarksAndSelectors(t *testing.T) {
	for _, in := range []string{"a\u200eb\u200fc\u061cd", "\u2764\ufe0f"} {
		assert.Equal(t, in, StripControlChars(in))
		assert.Equal(t, in, SafeLine(in))
		assert.Equal(t, in, SafeBlock(in))
	}
}
