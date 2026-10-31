// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-112.26 rejects the token class regardless of shell framing or expansion.
func TestWakeGuidance_InboxTokenClass_Checked(t *testing.T) {
	frames := []string{"%s", "(%s)", "( %s )", "{%s;}", "{ %s; }", "(exec %s)",
		"if true; then exec %s; fi", "while :; do timeout 5 %s; done", "! env %s",
		"case x in x) exec %s;; esac", "case x in\nx) %s;;\nesac", "if false; then date; else %s; fi",
		"if false; then date; elif %s; then date; fi", "until %s; do date; done",
		"date;%s", "date&&%s", "date||%s", "echo ready|%s", "%s|cat", "%s&&date", "%s||date",
		"sudo %s", "setsid %s", "arbitrary-prefix %s", "sh -c '%s'",
	}
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, frame := range frames {
		t.Run(frame, func(t *testing.T) {
			for _, head := range []string{`$M`, `${M}`, `${M:-mtix}`, `$(printf mtix)`, "`printf mtix`", "fetch", `"$M"`, "*"} {
				text := fmt.Sprintf(frame, head+" inbox")
				assertWakeTokenPolicy(t, text, false)
				require.NotEmpty(t, wakeLaunchProblems(script+"\n"+text), "%s", text)
			}
			for _, command := range []string{"date -u", "$M --help", "$TOOL run"} {
				assertWakeTokenPolicy(t, fmt.Sprintf(frame, command), true)
			}
		})
	}
}

func assertWakeTokenPolicy(t *testing.T, text string, accepted bool) {
	t.Helper()
	delimiter := strings.Repeat("`", strings.Count(text, "`")+2)
	for _, tt := range []struct {
		text string
		doc  bool
	}{{text, false}, {"Use " + delimiter + " " + text + " " + delimiter + ".", true}, {"```sh\n" + text + "\n```", true}} {
		require.Equal(t, accepted, len(wakeLaunchProblems(tt.text, tt.doc)) == 0, "%s", tt.text)
	}
	require.Empty(t, wakeLaunchProblems("An explanation of "+text+" is ordinary prose here.", true))
}

// Approved forms are whole logical lines, never prefixes of a different launch.
func TestWakeGuidance_InboxTokenAllowances_WholeLine(t *testing.T) {
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, line := range []string{
		`FRESH="$(mtix inbox --agent worker --format prompt)"`, `mtix inbox --agent worker --format prompt`,
		`mtix inbox lease acquire --agent worker --ttl 15m`, `HOLDER="$(mtix inbox lease acquire --agent worker --ttl 15m)"`,
		`if PAYLOAD=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt); then STATUS=0; else STATUS=$?; fi`,
	} {
		t.Run(line, func(t *testing.T) {
			assertWakeTokenPolicy(t, line, true)
			for _, text := range []string{line + "; $M inbox", "$M inbox; " + line, line + "; case x in x) $M inbox;; esac"} {
				assertWakeTokenPolicy(t, text, false)
				require.NotEmpty(t, wakeLaunchProblems(script+"\n"+text), "%s", text)
			}
		})
	}
}

// Literal inbox data in executable markup shares the conservative refusal.
func TestWakeGuidance_InboxData_Conservative(t *testing.T) {
	for _, text := range []string{"echo inbox", "cd inbox", "ls inbox", "git add inbox", `printf '%s\n' inbox`,
		"command -v fetch inbox", "command -V fetch inbox", "bash script inbox", `fetch $'inbox'`, `fetch in""box`, `fetch in\box`, "$ inbox", "`x` inbox",
	} {
		assertWakeTokenPolicy(t, text, false)
	}
	for _, text := range []string{"git add input", `printf '%s\n' constant`, "command -v fetch", "bash script input"} {
		assertWakeTokenPolicy(t, text, true)
	}
}

func TestWakeGuidance_ProtocolStatus_WholeLine(t *testing.T) {
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	line := `if [ "$STATUS" -ne 0 ]; then report "inbox read failed ($STATUS)"; exit "$STATUS"; fi`
	require.Empty(t, wakeLaunchProblems(script+"\n"+line))
	for _, text := range []string{line + "; $M inbox", "$M inbox; " + line} {
		require.NotEmpty(t, wakeLaunchProblems(script+"\n"+text), "%s", text)
	}
	hint := `ack: mtix inbox ack <seq> --agent <id> -C '<board>'`
	assertWakeTokenPolicy(t, hint, true)
	assertWakeTokenPolicy(t, hint+"; $M inbox", false)
}

// Metadata captures must not lend their exemption to a separate reader.
func TestWakeGuidance_MetadataProvenance_WholeCommand(t *testing.T) {
	for _, command := range []string{
		`mtix --json inbox lease acquire --agent worker`,
		`mtix -C board inbox lease acquire --agent worker`,
		`mtix --dir=board inbox ack 1 --agent worker`,
		`"$MTIX_BIN" inbox lease acquire --agent worker 2>/dev/null`,
	} {
		require.Empty(t, wakeSourceCaptures("VALUE=$("+command+")"), "%s", command)
		for _, suffix := range []string{"; $M inbox", " && fetch inbox", " | fetch inbox"} {
			require.NotEmpty(t, wakeSourceCaptures("VALUE=$("+command+suffix+")"))
		}
	}
	for _, command := range []string{`fetch inbox lease acquire`, `"$M" inbox ack 1`, `mtix inbox --agent worker`} {
		require.NotEmpty(t, wakeSourceCaptures("VALUE=$("+command+")"))
	}
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, line := range []string{
		`if true; then printf inbox; fi`,
		`if true; then mtix inbox lease acquire --agent worker; printf inbox; fi`,
		"read_queue() {\n mtix inbox lease acquire --agent worker\n printf inbox\n}",
	} {
		require.NotEmpty(t, wakeLaunchProblems(script+"\n"+line), "%s", line)
	}
}
