// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-189: every command an inbox envelope displays carries the board the
// events were read from, so an agent acting from another repository acts on
// the right board.
package format

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

const testBoard = "/work/lab board/it's"

const testBoardArg = ` -C '/work/lab board/it'\''s'`

var displayedCommand = regexp.MustCompile(`mtix (inbox|comment|show) `)

// assertEveryCommandPinned checks each displayed mtix command line ends with
// the board word, or carries it before the overflow line's closing words.
func assertEveryCommandPinned(t *testing.T, text string) {
	t.Helper()
	commands := 0
	for _, line := range strings.Split(text, "\n") {
		for _, part := range strings.Split(line, "; ") {
			if !displayedCommand.MatchString(part) || strings.HasPrefix(part, "body: ") {
				continue
			}
			commands++
			assert.True(t, strings.HasSuffix(part, testBoardArg) || strings.Contains(part, testBoardArg+" or increase"),
				"displayed command is pinned to the board: %q", part)
		}
	}
	assert.Positive(t, commands)
}

func TestRenderOn_EveryDisplayedCommandCarriesBoard(t *testing.T) {
	events := []Event{
		{Seq: 1, Task: "TEST-1", Sender: "sender", Body: "short"},
		{Seq: 2, Task: "TEST-2", Sender: "sender", Body: strings.Repeat("long ", 40)},
		{Seq: 3, Task: "TEST-3", Sender: "sender", Body: "hidden by the event cap"},
	}
	caps := Caps{MaxEvents: 2, MaxBodyBytes: 32}
	for _, tc := range []struct {
		name   string
		render func(string, string, []Event, Caps) (string, error)
	}{{"envelope", RenderEnvelopeOn}, {"prompt", RenderPromptOn}} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := tc.render("worker", testBoard, events, caps)
			require.NoError(t, err)
			assert.Contains(t, text, "ack: mtix inbox ack 1 --agent 'worker'"+testBoardArg+"\n")
			assert.Contains(t, text, `reply: mtix comment TEST-1 --to sender "<text>"`+testBoardArg+"\n")
			assert.Contains(t, text, "full text: mtix show TEST-2"+testBoardArg+";")
			assert.Contains(t, text, "read full events: mtix inbox --agent 'worker' --json"+testBoardArg+"\n")
			assert.Contains(t, text, "Read full events with mtix inbox --agent 'worker' --json"+testBoardArg+" or increase the caps.")
			if tc.name == "prompt" {
				assert.True(t, strings.HasPrefix(text, "mtix: new messages for agent 'worker' (seq 1, 2). Run: mtix inbox --agent 'worker' --format context"+testBoardArg+"\n"))
			}
			assertEveryCommandPinned(t, text)
		})
	}
}

func TestRenderOn_EmptyBoard_MatchesUnpinnedRender(t *testing.T) {
	events := []Event{{Seq: 4, Task: "TEST-1", Sender: "sender", Body: "x"}}
	pinned, err := RenderPromptOn("worker", "", events, Caps{})
	require.NoError(t, err)
	plain, err := RenderPrompt("worker", events, Caps{})
	require.NoError(t, err)
	assert.Equal(t, plain, pinned)
	assert.NotContains(t, pinned, " -C ")
}

func TestBoardSelection_ValidatesBoard(t *testing.T) {
	cases := []struct {
		name, board, want string
		bad               bool
	}{
		{"empty means none", "", "", false},
		{"absolute", "/srv/board", " -C '/srv/board'", false},
		{"apostrophe is quoted", "/a'b", ` -C '/a'\''b'`, false},
		{"shell metacharacters stay inside one word", "/a;$(x) `y`", " -C '/a;$(x) `y`'", false},
		{"relative", "board", "", true},
		{"newline", "/a\nack: mtix inbox ack 1", "", true},
		{"bidi control", "/a\u202eb", "", true},
		{"zero width", "/a\u200bb", "", true},
		{"invalid utf8", "/a\xffb", "", true},
		{"too long", "/" + strings.Repeat("a", maxBoardPathBytes), "", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BoardSelection(tt.board)
			if tt.bad {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				assert.Empty(t, got)
				_, renderErr := RenderEnvelopeOn("worker", tt.board, []Event{{Seq: 1, Task: "TEST-1", Sender: "s"}}, Caps{})
				require.ErrorIs(t, renderErr, model.ErrInvalidInput, "a bad board is never displayed")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRenderOn_TotalCap_IncludesBoardFraming(t *testing.T) {
	agent := strings.Repeat("'", 64)
	events := []Event{{Seq: 1, Task: "TEST-1", Sender: "sender", Body: strings.Repeat("hello", 100)}}
	board := "/" + strings.Repeat("b", 200)
	for _, render := range []func(string, string, []Event, Caps) (string, error){RenderEnvelopeOn, RenderPromptOn} {
		text, err := render(agent, board, events, Caps{MaxTotalBytes: MinimumEnvelopeBytes})
		require.ErrorIs(t, err, model.ErrInvalidInput, "framing that cannot fit fails loudly")
		assert.Contains(t, err.Error(), "max_total_bytes")
		assert.Empty(t, text)
		for _, limit := range []int{900, 1200, 4096} {
			text, err = render(agent, board, events, Caps{MaxTotalBytes: limit})
			require.NoError(t, err)
			assert.LessOrEqual(t, len(text), limit, "the cap counts the board bytes")
			assert.Contains(t, text, board)
		}
		text, err = render("worker", "/srv/board", events, Caps{MaxTotalBytes: MinimumEnvelopeBytes})
		require.NoError(t, err, "a typical board fits the minimum cap")
		assert.LessOrEqual(t, len(text), MinimumEnvelopeBytes)
	}
}
