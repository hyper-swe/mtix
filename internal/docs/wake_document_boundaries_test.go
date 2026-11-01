// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Document boundary tests preserve command framing and reference semantics.
package docs

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWakeGuidance_FenceInfo_PreservesFollowingProse(t *testing.T) {
	for _, info := range []string{"bash copy", "sh {.example}", " Bash", "Bash", " sh", "console", ""} {
		t.Run(info, func(t *testing.T) {
			prefix := "```" + info + "\n"
			prose := "\nretain \"$PAYLOAD\" as text"
			require.NotEmpty(t, wakeLaunchProblems(prefix+`courier "$PAYLOAD"`+"\n```", true))
			require.Empty(t, wakeLaunchProblems(prefix+"date -u\n```"+prose, true))
		})
	}
}

func TestWakeGuidance_DelimiterBoundaries_Checked(t *testing.T) {
	tests := []struct {
		name, text string
		allowed    bool
	}{
		{"Kind", "~~~sh\ndate -u\n```\ncourier \"$PAYLOAD\"\n~~~", false},
		{"ShortCloser", "````sh\ndate -u\n```\ncourier \"$PAYLOAD\"\n````", false},
		{"LongCloser", "```sh\ndate -u\n````\nretain \"$PAYLOAD\" as text", true},
		{"CloserInfo", "```sh\ndate -u\n```bash\ncourier \"$PAYLOAD\"\n```", false},
		{"ParagraphBreak", "Words `courier\n\n\"$PAYLOAD\"` end.", true},
		{"SpanRun", "Use `mtix inbox --format prompt `` courier \"$PAYLOAD\"`.", false},
		{"SpanRunReference", "Use `mtix inbox --format prompt `` $PAYLOAD`.", false},
		{"Continuation", "```sh\nprintf '%s\\n' \"$PAYLOAD\" \\\n | claude -p\n```", true},
		{"ReferencePath", "Keep `$PAYLOAD/path` as a reference.", true},
		{"ForwardedReference", "Read `command -- mtix inbox --agent worker --format prompt`.", true},
		{"FunctionReference", "See `Concurrent()`.\n\nRead `mtix inbox --format prompt`.", true},
		{"AcknowledgmentHint", "Use `ack: mtix inbox ack <seq> --agent <id> -C '<board>'`.", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { require.Equal(t, tt.allowed, len(wakeLaunchProblems(tt.text, true)) == 0) })
	}
}

func TestWakeGuidance_ReferencePipe_DistinguishesOperands(t *testing.T) {
	for _, command := range []string{`mtix inbox --format prompt\|context`, `command -- mtix inbox --format prompt\|context`} {
		t.Run(command, func(t *testing.T) {
			require.Empty(t, wakeLaunchProblems(fmt.Sprintf("Read `%s`.", command), true))
		})
	}
	for _, command := range []string{`mtix inbox --format prompt|context`, `mtix inbox --format prompt | context`} {
		t.Run(command, func(t *testing.T) {
			require.NotEmpty(t, wakeLaunchProblems(fmt.Sprintf("Read `%s`.", command), true))
		})
	}
}

func TestWakeGuidance_MultilineSpan_PreservesSource(t *testing.T) {
	text := "Capture `FRESH=\"$(mtix\ninbox --agent worker --format prompt)\"`.\nThen use `courier \"$FRESH\"`."
	require.Equal(t, []string{`FRESH="$(mtix inbox --agent worker --format prompt)"`, `courier "$FRESH"`}, wakeCommandLines(text, true))
	require.NotEmpty(t, wakeLaunchProblems(text, true))
}
