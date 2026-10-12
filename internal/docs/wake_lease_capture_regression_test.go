// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Lease guidance checks preserve captured input across compound command contexts.
package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWakeLeaseProtocol_CompoundCaptureConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	captures := []struct{ name, text string }{
		{"FollowingLease", `UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt)`},
		{"MultipleAssignments", `FIRST=0; UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt)`},
		{"FollowingMetadata", `UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); STAMP=$(date +%s); INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt)`},
	}
	consumers := []struct{ name, text string }{
		{"Report", `report "$INPUT"`},
		{"Printf", `printf "$INPUT\n" >&2`},
		{"Trap", `trap 'report "$INPUT"' EXIT`},
		{"Alias", `MESSAGE="$INPUT"; report "$MESSAGE"`},
	}
	for _, capture := range captures {
		for _, consumer := range consumers {
			t.Run(capture.name+"/"+consumer.name, func(t *testing.T) {
				form := capture.text + "\n" + consumer.text
				require.NotEmpty(t, wakeLaunchProblems(source+"\n"+form))
			})
		}
	}
}

func TestWakeLeaseProtocol_SeparatedCaptureConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	capture := `UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt)`
	for _, consumer := range []struct{ name, text string }{{"Report", `report "$INPUT"`}, {"Printf", `printf "$INPUT\n" >&2`}, {"Trap", `trap 'report "$INPUT"' EXIT`}, {"Alias", `MESSAGE="$INPUT"; report "$MESSAGE"`}} {
		t.Run(consumer.name, func(t *testing.T) {
			require.NotEmpty(t, wakeLaunchProblems(source+"\n"+strings.ReplaceAll(capture, "; ", "\n")+"\n"+consumer.text))
			require.NotEmpty(t, wakeLaunchProblems(capture+"\n"+consumer.text))
		})
	}
}

func TestWakeLeaseProtocol_NestedCaptureConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, form := range []struct{ name, text string }{
		{"NestedAssignment", `OUTER=$(UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt); report "$INPUT")`},
		{"ConditionalCapture", `if UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); then INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt); report "$INPUT"; fi`},
		{"CompoundAliasTrap", `UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt); MESSAGE="$INPUT"; trap 'report "$MESSAGE"' EXIT`},
	} {
		t.Run(form.name, func(t *testing.T) { require.NotEmpty(t, wakeLaunchProblems(source+"\n"+form.text)) })
	}
}

func TestWakeLeaseProtocol_CommandLocalMetadata_Accepted(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, form := range []struct{ name, text string }{
		{"LeaseTimestamp", `UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); STAMP=$(date +%s); report "$STAMP"`},
		{"ReleaseOutcome", `OUTCOME=$("$MTIX_BIN" inbox lease release --agent "$AGENT" --holder "$HOLDER"); printf '%s\n' "$OUTCOME" >&2`},
		{"TimestampTrap", `STAMP=$(date +%s); trap 'report "$STAMP"' EXIT`},
		{"NestedTimestamp", `OUTER=$(STAMP=$(date +%s); report "$STAMP")`},
	} {
		t.Run(form.name, func(t *testing.T) { require.Empty(t, wakeLaunchProblems(source+"\n"+form.text)) })
	}
}

func TestWakeInputVariables_MultipleCapturesAndAliases_PreservesEachSource(t *testing.T) {
	lines := []string{
		`UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); FIRST=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt); SECOND=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt)`,
		`MESSAGE="$ALIAS"; ALIAS="${FIRST}"`,
	}
	inputs := wakeInputVariables(lines)
	for _, name := range []string{"FIRST", "SECOND", "ALIAS", "MESSAGE"} {
		require.True(t, inputs[name], "capture or alias %s", name)
	}
	require.False(t, inputs["UNUSED"])
}

func TestWakeProtocolMetadataLine_CompoundCommands_RequiresEachHead(t *testing.T) {
	for _, fixture := range []struct {
		name, text string
		allowed    bool
	}{
		{"LeaseAndTimestamp", `UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); STAMP=$(date +%s)`, true},
		{"FollowingReader", `UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt)`, false},
		{"PrecedingReader", `INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt); UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT")`, false},
		{"NestedReader", `OUTER=$(UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT"); INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt))`, false},
		{"DirectReader", `"$MTIX_BIN" inbox lease acquire --agent "$AGENT"; "$MTIX_BIN" inbox --agent "$AGENT" --format prompt`, false},
	} {
		t.Run(fixture.name, func(t *testing.T) { require.Equal(t, fixture.allowed, wakeProtocolMetadataLine(fixture.text)) })
	}
}

func TestWakeMetadataSubstitutions_IndependentCaptures_RequiresEachHead(t *testing.T) {
	lease := `UNUSED=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT")`
	require.True(t, wakeMetadataSubstitutions(lease+`; STAMP=$(date +%s)`))
	reader := `INPUT=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt)`
	require.False(t, wakeMetadataSubstitutions(lease+`; `+reader))
	require.False(t, wakeMetadataSubstitutions(`OUTER=$(`+lease+`; `+reader+`)`))
}
