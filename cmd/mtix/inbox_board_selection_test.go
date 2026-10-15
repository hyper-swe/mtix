// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-189: an agent that reads its inbox through -C on one board and acks
// from inside another repository (its own .mtix) must still ack on the board
// it read. Every mtix command printed by --format prompt and context carries
// the board it was read from, and the ack refusal names the board it checked.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const boardSelectionAgent = "worker"

// newInboxBoard creates a project directory with its own .mtix board holding
// TEST-1 and n comments addressed to boardSelectionAgent. It returns the
// resolved absolute project root.
func newInboxBoard(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	dataDir := filepath.Join(resolved, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(resolved, ".mtix", "config.yaml"),
		[]byte("prefix: TEST\nsync:\n  auto_sync: false\n"), 0o600))
	store, err := sqlite.New(filepath.Join(dataDir, "mtix.db"), slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.CreateNode(t.Context(), &model.Node{
		ID: "TEST-1", Project: "TEST", Seq: 1, Title: "board selection target",
		NodeType: model.NodeTypeStory, Status: model.StatusOpen, Weight: 1, Creator: "tester",
		CreatedAt: now, UpdatedAt: now,
	}))
	annotations := make([]model.Annotation, 0, n)
	for i := 0; i < n; i++ {
		annotations = append(annotations, model.Annotation{
			ID: fmt.Sprintf("board-event-%d", i), Author: "sender", Text: fmt.Sprintf("event %d", i),
			Addressee: boardSelectionAgent, CreatedAt: now.Add(time.Duration(i) * time.Minute),
		})
		require.NoError(t, store.SetAnnotations(t.Context(), "TEST-1", annotations))
	}
	return resolved
}

// runMtixIn runs the real root command with cwd set to dir, as an agent
// shell in that repository would, and returns stdout and the command error.
func runMtixIn(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	saveAndResetApp(t)
	t.Chdir(dir)
	var runErr error
	out := captureStdout(t, func() {
		root := newRootCmd()
		root.SetArgs(args)
		runErr = root.ExecuteContext(t.Context())
	})
	return out, runErr
}

func boardInbox(t *testing.T, board string) []sqlite.InboxEvent {
	t.Helper()
	store, err := sqlite.New(filepath.Join(board, ".mtix", "data", "mtix.db"), slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	events, err := store.InboxList(t.Context(), boardSelectionAgent)
	require.NoError(t, err)
	return events
}

var boardAckLine = regexp.MustCompile(`(?m)^ack: mtix inbox ack ([1-9][0-9]*) --agent 'worker' -C '([^']+)'$`)

func TestInboxFormat_AckFromOtherRepository_AcksOnBoardRead(t *testing.T) {
	identityOperatorState(t)
	boardA := newInboxBoard(t, 4)
	boardB := newInboxBoard(t, 0) // the agent's own repository, with a shorter journal
	for _, format := range []string{"context", "prompt"} {
		t.Run(format, func(t *testing.T) {
			before := boardInbox(t, boardA)
			require.NotEmpty(t, before)
			out, err := runMtixIn(t, boardB, "-C", boardA, "inbox", "--agent", boardSelectionAgent, "--format", format)
			require.NoError(t, err)
			quotedA := "-C '" + boardA + "'"
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "mtix ") {
					assert.True(t, strings.HasSuffix(line, quotedA) || strings.Contains(line, quotedA+" "),
						"every printed mtix command selects the board it was read from: %q", line)
				}
			}
			if format == "prompt" {
				assert.Contains(t, out, "--format context "+quotedA, "the nudge reads the same board")
			}
			assert.Contains(t, out, `reply: mtix comment TEST-1 --to sender "<text>" `+quotedA)
			matches := boardAckLine.FindAllStringSubmatch(out, -1)
			require.Len(t, matches, len(before), "one board-pinned ack line per event:\n%s", out)
			first := matches[0]
			assert.Equal(t, boardA, first[2])
			// The agent pastes the ack line from inside its own repository.
			_, err = runMtixIn(t, boardB, "inbox", "ack", first[1], "--agent", boardSelectionAgent, "-C", first[2])
			require.NoError(t, err, "the printed ack line acks on the board it was read from")
			assert.Len(t, boardInbox(t, boardA), len(before)-1, "the event left board A's inbox")
		})
	}
}

func TestInboxAck_OutsideJournal_NamesBoardAndHintsC(t *testing.T) {
	identityOperatorState(t)
	boardA := newInboxBoard(t, 4)
	boardB := newInboxBoard(t, 0)
	events := boardInbox(t, boardA)
	require.NotEmpty(t, events)
	seq := fmt.Sprint(events[len(events)-1].Seq)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"single", []string{"inbox", "ack", seq, "--agent", boardSelectionAgent}},
		{"through", []string{"inbox", "ack", seq, "--through", "--agent", boardSelectionAgent}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runMtixIn(t, boardB, tc.args...)
			require.Error(t, err)
			assert.ErrorIs(t, err, model.ErrInvalidInput, "the exit-code class is unchanged")
			msg := err.Error()
			assert.Contains(t, msg, "outside the journal")
			assert.Contains(t, msg, "board checked: "+boardB)
			assert.Contains(t, msg, "may belong to another board")
			assert.Contains(t, msg, "mtix -C <board> inbox ack")
			assert.Len(t, boardInbox(t, boardA), len(events), "nothing was acked anywhere")
		})
	}
}

func TestInboxAck_OtherErrors_NotAnnotatedWithBoard(t *testing.T) {
	identityOperatorState(t)
	board := newInboxBoard(t, 1)
	_, err := runMtixIn(t, board, "inbox", "ack", "x1", "--agent", boardSelectionAgent)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "board checked")
}

func TestInboxListing_AckHint_PinsBoardRead(t *testing.T) {
	identityOperatorState(t)
	boardA := newInboxBoard(t, 2)
	boardB := newInboxBoard(t, 0)
	events := boardInbox(t, boardA)
	require.Len(t, events, 2)
	out, err := runMtixIn(t, boardB, "-C", boardA, "inbox", "--agent", boardSelectionAgent)
	require.NoError(t, err)
	last := fmt.Sprint(events[len(events)-1].Seq)
	assert.Contains(t, out, "ack with: mtix inbox ack "+last+" --agent worker -C '"+boardA+"'\n",
		"the plain listing's ack hint selects the board it was read from")
}
