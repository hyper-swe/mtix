// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Forwarded command checks retain input identity and command-local metadata rules.
package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func forwardedReaderFixtures() []struct{ name, text string } {
	return []struct{ name, text string }{
		{"ForwardedVariable", `INPUT=$(command "$MTIX_BIN" inbox --agent "$AGENT" --format prompt)`},
		{"ForwardedSeparator", `INPUT=$(command -- "$MTIX_BIN" inbox --agent "$AGENT" --format prompt)`},
		{"ForwardedLiteral", `INPUT=$(command mtix inbox --agent "$AGENT" --format prompt)`},
		{"ForwardedBacktick", "INPUT=`command \"$MTIX_BIN\" inbox --agent \"$AGENT\" --format prompt`"},
		{"ForwardedPipeline", `INPUT=$(command "$MTIX_BIN" inbox --agent "$AGENT" --format prompt | command cat)`},
		{"ForwardedLogical", `INPUT=$(command "$MTIX_BIN" inbox --agent "$AGENT" --format prompt && date +%s)`},
		{"NestedOperand", `INPUT=$(printf '%s' "$(command "$MTIX_BIN" inbox --agent "$AGENT" --format prompt)")`},
	}
}

func forwardedConsumerFixtures() []struct{ name, text string } {
	return []struct{ name, text string }{
		{"Report", `report "$INPUT"`},
		{"VariableFormat", `printf "$INPUT\n" >&2`},
		{"Trap", `trap 'report "$INPUT"' EXIT`},
		{"TwoAliases", `FIRST="$INPUT"; SECOND="$FIRST"; trap 'report "$SECOND"' EXIT`},
	}
}

func TestWakeLeaseProtocol_ForwardedCapturedConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, capture := range forwardedReaderFixtures() {
		for _, consumer := range forwardedConsumerFixtures() {
			t.Run(capture.name+"/"+consumer.name, func(t *testing.T) {
				require.NotEmpty(t, wakeLaunchProblems(source+"\nUNUSED=$(\"$MTIX_BIN\" inbox lease acquire --agent \"$AGENT\"); "+capture.text+"\n"+consumer.text))
			})
		}
	}
}

func TestWakeInputVariables_ForwardedCaptures_PreservesSourceAndAliases(t *testing.T) {
	for _, capture := range forwardedReaderFixtures() {
		t.Run(capture.name, func(t *testing.T) {
			inputs := wakeInputVariables([]string{capture.text, `FIRST="$INPUT"; SECOND="$FIRST"`})
			require.True(t, inputs["INPUT"])
			require.True(t, inputs["FIRST"])
			require.True(t, inputs["SECOND"])
		})
	}
}

func TestWakeLeaseProtocol_SeparatedForwardedConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, capture := range forwardedReaderFixtures() {
		t.Run(capture.name, func(t *testing.T) {
			require.NotEmpty(t, wakeLaunchProblems(source+"\nUNUSED=$(\"$MTIX_BIN\" inbox lease acquire --agent \"$AGENT\")\n"+capture.text+"\nreport \"$INPUT\""))
		})
	}
}

func TestWakeLeaseProtocol_DirectCapturedConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, consumer := range forwardedConsumerFixtures() {
		t.Run(consumer.name, func(t *testing.T) {
			require.NotEmpty(t, wakeLaunchProblems(source+"\nINPUT=$(\"$MTIX_BIN\" inbox --agent \"$AGENT\" --format prompt)\n"+consumer.text))
		})
	}
}

func TestWakeLeaseProtocol_IndependentConsumerFragments_Refused(t *testing.T) {
	for _, consumer := range forwardedConsumerFixtures() {
		t.Run(consumer.name, func(t *testing.T) {
			require.NotEmpty(t, wakeLaunchProblems(strings.ReplaceAll(consumer.text, "INPUT", "PAYLOAD")))
		})
	}
}

func TestWakeLeaseProtocol_ForwardedLocalMetadata_Accepted(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, fixture := range []struct{ name, text string }{
		{"Date", `STAMP=$(command date +%s); report "$STAMP"`},
		{"Lease", `OUTCOME=$(command "$MTIX_BIN" inbox lease acquire --agent "$AGENT"); printf '%s\n' "$OUTCOME" >&2`},
		{"Release", `OUTCOME=$(command -- "$MTIX_BIN" inbox lease release --agent "$AGENT" --holder "$HOLDER"); printf '%s\n' "$OUTCOME" >&2`},
		{"Lookup", `command -v "$MTIX_BIN" >/dev/null`},
		{"DirectDate", `STAMP=$(date +%s); report "$STAMP"`},
		{"DirectLease", `OUTCOME=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); printf '%s\n' "$OUTCOME" >&2`},
	} {
		t.Run(fixture.name, func(t *testing.T) { require.Empty(t, wakeLaunchProblems(source+"\n"+fixture.text)) })
	}
}
