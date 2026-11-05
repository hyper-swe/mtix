// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestAwaitReplyValue_Table_PositionalDurationOnlyWithBareFlag(t *testing.T) {
	tests := []struct {
		name    string
		set     bool
		flag    string
		args    []string
		want    string
		wantErr bool
	}{
		{"no flag, two args", false, "", []string{"N-1", "text"}, "", false},
		{"flag with =value", true, "30m", []string{"N-1", "text"}, "30m", false},
		{"bare flag, two args means the default", true, awaitReplyDefault, []string{"N-1", "text"}, awaitReplyDefault, false},
		{"bare flag takes a third arg as the duration", true, awaitReplyDefault, []string{"N-1", "text", "45m"}, "45m", false},
		{"third arg without the flag is refused", false, "", []string{"N-1", "text", "45m"}, "", true},
		{"third arg after an explicit value is refused", true, "30m", []string{"N-1", "text", "45m"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := awaitReplyValue(tt.set, tt.flag, tt.args)
			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func replyWindowsOf(t *testing.T) (windows int, replyBy time.Time) {
	t.Helper()
	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	st, err := src.state.ReadReplyState(src.key())
	require.NoError(t, err)
	if len(st.Windows) > 0 {
		replyBy = st.Windows[0].ReplyBy
	}
	return len(st.Windows), replyBy
}

func TestRunComment_AwaitReply_Table_DurationsAndWindow(t *testing.T) {
	tests := []struct {
		name string
		wait string
		want time.Duration
	}{
		{"default is one hour", awaitReplyDefault, time.Hour},
		{"explicit thirty minutes", "30m", 30 * time.Minute},
		{"three days clamps to eight hours", "72h", 8 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			id := seedOneNode(t)
			before := time.Now().UTC()
			out := captureStdout(t, func() {
				require.NoError(t, runCommentWith(id, "need an answer", commentOptions{To: "peer", Await: tt.wait, AwaitSet: true}))
			})
			assert.Contains(t, out, "reply window: open until")
			n, replyBy := replyWindowsOf(t)
			require.Equal(t, 1, n)
			assert.WithinDuration(t, before.Add(tt.want), replyBy, time.Minute)
			evs, err := app.store.ReplyByEvents(context.Background(), 0, 10)
			require.NoError(t, err)
			require.Len(t, evs, 1, "the addressed event carries reply_by")
			assert.Equal(t, model.FormatReplyBy(replyBy), evs[0].ReplyBy)
		})
	}
}

func TestRunComment_AwaitReply_JSONReportsDeadline(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true
	id := seedOneNode(t)
	out := captureStdout(t, func() {
		require.NoError(t, runCommentWith(id, "q", commentOptions{To: "peer", Await: "20m", AwaitSet: true}))
	})
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &got))
	assert.NotEmpty(t, got["reply_by"])
	assert.Contains(t, got["reply_window"], "open until")
}

func TestRunComment_AwaitReply_Refusals_WriteNothing(t *testing.T) {
	tests := []struct {
		name string
		opt  commentOptions
	}{
		{"without --to", commentOptions{Await: "30m", AwaitSet: true}},
		{"zero duration", commentOptions{To: "peer", Await: "0s", AwaitSet: true}},
		{"unparseable duration", commentOptions{To: "peer", Await: "soon", AwaitSet: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			id := seedOneNode(t)
			require.ErrorIs(t, runCommentWith(id, "q", tt.opt), model.ErrInvalidInput)
			n, _ := replyWindowsOf(t)
			assert.Zero(t, n)
			evs, err := app.store.ReplyByEvents(context.Background(), 0, 10)
			require.NoError(t, err)
			assert.Empty(t, evs, "a refused send writes nothing")
		})
	}
}

func TestRunComment_NoAwaitReply_OpensNoWindow(t *testing.T) {
	initTestApp(t)
	id := seedOneNode(t)
	require.NoError(t, runComment(id, "fyi", "peer"))
	n, _ := replyWindowsOf(t)
	assert.Zero(t, n, "scale-to-zero: a plain addressed send opens no reply window")
}

func TestCommentCmd_AwaitReplyFlag_AllThreeSpellings(t *testing.T) {
	for _, args := range [][]string{
		{"--to", "peer", "--await-reply"},
		{"--to", "peer", "--await-reply=45m"},
		{"--to", "peer", "--await-reply", "45m"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			initTestApp(t)
			id := seedOneNode(t)
			captureStdout(t, func() {
				_, err := executeCmd(newCommentCmd(), append([]string{id, "need an answer"}, args...)...)
				require.NoError(t, err)
			})
			n, _ := replyWindowsOf(t)
			assert.Equal(t, 1, n)
		})
	}
	initTestApp(t)
	cmd := newCommentCmd()
	assert.Equal(t, "default", cmd.Flags().Lookup("await-reply").NoOptDefVal)
	assert.Contains(t, cmd.Long, "default 1h, at most 8h")
}
