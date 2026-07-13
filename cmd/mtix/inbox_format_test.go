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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	out := formatInboxPrompt("developer", sampleInbox())

	// The payload tells the woken agent who it is and how to close the loop.
	assert.Contains(t, out, `agent "developer"`)
	assert.Contains(t, out, "2 unread")
	assert.Contains(t, out, "mtix inbox ack <seq> --agent developer", "the ack contract is part of the prompt")
	assert.Contains(t, out, "mtix comment <node-id> --to <sender>", "the reply contract is part of the prompt")

	// Every event arrives verbatim with its routing facts.
	assert.Contains(t, out, "[seq 41] PROJ-7 from planner: Start on PROJ-7. Plan context attached.")
	assert.Contains(t, out, "[seq 45] PROJ-9 from tester: PROJ-9 failed AC-3, see notes.")
}

func TestFormatInboxContext_CompactInjection(t *testing.T) {
	out := formatInboxContext("developer", sampleInbox())

	assert.Contains(t, out, `mtix inbox for agent "developer": 2 unread`)
	assert.Contains(t, out, "- [41] PROJ-7 from planner: Start on PROJ-7. Plan context attached.")
	assert.Contains(t, out, "- [45] PROJ-9 from tester: PROJ-9 failed AC-3, see notes.")
	assert.Contains(t, out, "mtix inbox ack <seq> --agent developer")
}

func TestFormatInbox_EmptyMeansEmptyOutput(t *testing.T) {
	// The scriptable contract: empty inbox -> zero bytes, so a wake script's
	// `[ -z "$payload" ]` is the idempotency check.
	assert.Empty(t, formatInboxPrompt("developer", nil))
	assert.Empty(t, formatInboxContext("developer", nil))
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
