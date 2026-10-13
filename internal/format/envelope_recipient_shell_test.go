// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Recipient commands preserve the local-agent contract through a real POSIX shell.
package format

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertShellRecipient(t *testing.T, command, recipient string) {
	t.Helper()
	assert.Contains(t, command, "--agent '", "recipient is always one quoted word")
	syntax := exec.CommandContext(t.Context(), "/bin/sh", "-n", "-c", command)
	output, err := syntax.CombinedOutput()
	require.NoError(t, err, "shell syntax: %s", output)
	script := `mtix() { while [ "$#" -gt 0 ]; do if [ "$1" = "--agent" ]; then shift; printf '%s' "$1"; return; fi; shift; done; return 17; }; ` + command
	parse := exec.CommandContext(t.Context(), "/bin/sh", "-c", script)
	output, err = parse.CombinedOutput()
	require.NoError(t, err, "shell parse: %s", output)
	assert.Equal(t, recipient, string(output))
}

func recipientCommands(t *testing.T, recipient string) []string {
	t.Helper()
	nudge, err := RenderNudge(recipient, []int64{1})
	require.NoError(t, err)
	_, command, ok := strings.Cut(nudge, "Run: ")
	require.True(t, ok)
	commands := []string{command}
	input := []Event{{Seq: 1, Task: "TEST-1", Sender: "sender", Body: "hello"}, {Seq: 2, Task: "TEST-2", Sender: "sender", Body: "later"}}
	prompt, err := RenderPrompt(recipient, input[:1], Caps{})
	require.NoError(t, err)
	first, _, _ := strings.Cut(prompt, "\n")
	_, command, ok = strings.Cut(first, "Run: ")
	require.True(t, ok)
	commands = append(commands, command)
	envelope, err := RenderEnvelope(recipient, input, Caps{MaxEvents: 1})
	require.NoError(t, err)
	for _, line := range strings.Split(envelope, "\n") {
		if strings.HasPrefix(line, "ack: ") {
			commands = append(commands, strings.TrimPrefix(line, "ack: "))
		}
		if _, tail, found := strings.Cut(line, "Read full events with "); found {
			tail, _, _ = strings.Cut(tail, " or increase the caps.")
			commands = append(commands, tail)
		}
	}
	input[0].Body = strings.Repeat("A", 100)
	envelope, err = RenderEnvelope(recipient, input[:1], Caps{MaxBodyBytes: 2})
	require.NoError(t, err)
	_, command, ok = strings.Cut(envelope, "read full events: ")
	require.True(t, ok)
	command, _, _ = strings.Cut(command, "\n")
	commands = append(commands, command)
	require.Len(t, commands, 5)
	return commands
}

func TestRenderRecipient_LocalAgentContract_ShellRoundTripsBytes(t *testing.T) {
	cases := []string{"worker", "TEST-agent", "Agent.User", "agent@example", " Agent User ", "O'Brien", "$MTIX_RECIPIENT", "`printf substituted`", "équipe-代理", strings.Repeat("a", 64), strings.Repeat("é", 32), strings.Repeat("'", 64)}
	for _, recipient := range cases {
		t.Run(recipient, func(t *testing.T) {
			for _, command := range recipientCommands(t, recipient) {
				assertShellRecipient(t, command, recipient)
			}
		})
	}
}
