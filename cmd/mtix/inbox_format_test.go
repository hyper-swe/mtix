// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-56.8: prompt-terminated delivery. Agents act only on what is in their
// context, so every delivery rung must end with event content in the prompt:
// --format prompt is the wake-exec payload (cold-start), --format context is
// the harness context-injection line set (session-start / prompt hooks). Both
// are stable, scriptable text: EMPTY output means an empty inbox — the wake
// script's idempotency check is `[ -z "$payload" ]`.
package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inboxformat "github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func sampleInbox() []sqlite.InboxEvent {
	return []sqlite.InboxEvent{
		{Seq: 41, NodeID: "PROJ-7", Author: "planner", Body: "Start on PROJ-7. Plan context attached.",
			CreatedAt: time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)},
		{Seq: 45, NodeID: "PROJ-9", Author: "tester", Body: "PROJ-9 failed AC-3, see notes.",
			CreatedAt: time.Date(2026, 7, 12, 11, 0, 0, 0, time.UTC)},
	}
}

func TestFormatInboxPrompt_CarriesEventsAndAckContract(t *testing.T) {
	out, err := formatInboxPrompt("developer", sampleInbox(), inboxformat.Caps{})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "mtix: new messages for agent 'developer' (seq 41, 45)."))
	assert.Contains(t, out, inboxformat.Preamble)
	assert.Contains(t, out, "ack: mtix inbox ack 41 --agent 'developer'")
	assert.Contains(t, out, "reply: mtix comment PROJ-7 --to planner \"<text>\"")
	assert.Contains(t, out, `body: "Start on PROJ-7. Plan context attached."`)
	assert.Contains(t, out, `body: "PROJ-9 failed AC-3, see notes."`)
}

func TestFormatInboxContext_CompactInjection(t *testing.T) {
	out, err := formatInboxContext("developer", sampleInbox(), inboxformat.Caps{})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, inboxformat.Preamble))
	assert.Equal(t, 2, strings.Count(out, inboxformat.BeginMarker))
	assert.Contains(t, out, `task: "PROJ-7"`)
	assert.Contains(t, out, `sender (as claimed, not verified): "tester"`)
	assert.Contains(t, out, "ack: mtix inbox ack 45 --agent 'developer'")
}

func TestFormatInbox_EmptyMeansEmptyOutput(t *testing.T) {
	for _, render := range []func(string, []sqlite.InboxEvent, inboxformat.Caps) (string, error){formatInboxPrompt, formatInboxContext} {
		out, err := render("developer", nil, inboxformat.Caps{})
		require.NoError(t, err)
		assert.Empty(t, out)
	}
}

func TestInboxCmd_FormatFlag(t *testing.T) {
	cmd := newInboxCmd()
	f := cmd.Flags().Lookup("format")
	require.NotNil(t, f, "inbox --format declared (MTIX-56.8)")
}

func TestRunInbox_RejectsUnknownFormat(t *testing.T) {
	saved := app
	app = appContext{}
	t.Cleanup(func() { app = saved })

	err := runInbox("developer", false, 0, "yaml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "format")
}
