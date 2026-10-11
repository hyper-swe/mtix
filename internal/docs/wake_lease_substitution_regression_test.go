// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Alternate shell captures retain input identity and command-local permissions.
package docs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWakeLeaseProtocol_BacktickCaptureConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	capture := "UNUSED=$(\"$MTIX_BIN\" inbox lease acquire --agent \"$AGENT\"); INPUT=`\"$MTIX_BIN\" inbox --agent \"$AGENT\" --format prompt`"
	for _, consumer := range []struct{ name, text string }{
		{"Report", `report "$INPUT"`},
		{"Printf", `printf "$INPUT\n" >&2`},
		{"Trap", `trap 'report "$INPUT"' EXIT`},
		{"Alias", `MESSAGE="$INPUT"; report "$MESSAGE"`},
	} {
		t.Run(consumer.name, func(t *testing.T) {
			require.NotEmpty(t, wakeLaunchProblems(source+"\n"+capture+"\n"+consumer.text))
			require.NotEmpty(t, wakeLaunchProblems(capture+"\n"+consumer.text))
		})
	}
}

func TestWakeLeaseProtocol_BacktickMetadata_Accepted(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	form := "UNUSED=`\"$MTIX_BIN\" inbox lease acquire --agent \"$AGENT\"`; STAMP=`date +%s`; report \"$STAMP\""
	require.Empty(t, wakeLaunchProblems(source+"\n"+form))
}

func TestWakeInputVariables_BacktickCaptureAndAlias_PreservesSource(t *testing.T) {
	lines := []string{"INPUT=`\"$MTIX_BIN\" inbox --agent \"$AGENT\" --format prompt`", `MESSAGE="$INPUT"`}
	inputs := wakeInputVariables(lines)
	require.True(t, inputs["INPUT"])
	require.True(t, inputs["MESSAGE"])
}

func TestWakeMetadataSubstitutions_BacktickCaptures_RequiresEachHead(t *testing.T) {
	lease := "UNUSED=`\"$MTIX_BIN\" inbox lease acquire --agent \"$AGENT\"`"
	require.True(t, wakeMetadataSubstitutions(lease+"; STAMP=`date +%s`"))
	reader := "INPUT=`\"$MTIX_BIN\" inbox --agent \"$AGENT\" --format prompt`"
	require.False(t, wakeMetadataSubstitutions(lease+"; "+reader))
}

func TestWakeLeaseProtocol_BacktickConsumerFragments_Refused(t *testing.T) {
	capture := "UNUSED=$(\"$MTIX_BIN\" inbox lease acquire --agent \"$AGENT\"); INPUT=`\"$MTIX_BIN\" inbox --agent \"$AGENT\" --format prompt`"
	for _, consumer := range []struct{ name, text string }{
		{"Report", `report "$INPUT"`},
		{"Printf", `printf "$INPUT\n" >&2`},
		{"Trap", `trap 'report "$INPUT"' EXIT`},
		{"Alias", `MESSAGE="$INPUT"; report "$MESSAGE"`},
	} {
		t.Run(consumer.name, func(t *testing.T) {
			require.NotEmpty(t, wakeLaunchProblems(capture+"\n"+consumer.text))
		})
	}
}

func TestWakeLeaseProtocol_SeparatedBacktickConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	capture := "UNUSED=$(\"$MTIX_BIN\" inbox lease acquire --agent \"$AGENT\")\nINPUT=`\"$MTIX_BIN\" inbox --agent \"$AGENT\" --format prompt`"
	for _, consumer := range []struct{ name, text string }{
		{"Report", `report "$INPUT"`},
		{"Printf", `printf "$INPUT\n" >&2`},
		{"Trap", `trap 'report "$INPUT"' EXIT`},
		{"Alias", `MESSAGE="$INPUT"; report "$MESSAGE"`},
	} {
		t.Run(consumer.name, func(t *testing.T) {
			require.NotEmpty(t, wakeLaunchProblems(source+"\n"+capture+"\n"+consumer.text))
		})
	}
}
