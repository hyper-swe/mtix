// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Extraction checks preserve shell assignment origins alongside document references.
package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func literalReaderFixtures() []struct {
	name, command string
	supported     bool
} {
	return []struct {
		name, command string
		supported     bool
	}{
		{"Direct", "mtix inbox --agent worker --format prompt", true},
		{"Forwarded", "command mtix inbox --agent worker --format prompt", true},
		{"SeparatedForwarding", "command -- mtix inbox --agent worker --format prompt", true},
		{"OtherForwarding", "command -p mtix inbox --agent worker --format prompt", false},
		{"EnvironmentForwarding", "env mtix inbox --agent worker --format prompt", false},
	}
}

func TestWakeCommandLines_LiteralCaptureAssignments_PreservesCompleteOrigin(t *testing.T) {
	for _, fixture := range literalReaderFixtures() {
		line := "INPUT=`" + fixture.command + "`"
		t.Run(fixture.name, func(t *testing.T) { require.Contains(t, wakeCommandLines(line), line) })
		t.Run(fixture.name+"/Comment", func(t *testing.T) { require.Contains(t, wakeCommandLines("# "+line), line) })
	}
}

func TestWakeInputVariables_LiteralReaderCaptures_PreservesSourceAndAliases(t *testing.T) {
	for _, fixture := range literalReaderFixtures() {
		if !fixture.supported {
			continue
		}
		t.Run(fixture.name, func(t *testing.T) {
			lines := wakeCommandLines("INPUT=`" + fixture.command + "`\nFIRST=\"$INPUT\"; SECOND=\"$FIRST\"")
			inputs := wakeInputVariables(lines)
			assert.True(t, inputs["INPUT"], "source")
			assert.True(t, inputs["FIRST"], "first alias")
			assert.True(t, inputs["SECOND"], "second alias")
		})
	}
}

func TestWakeLeaseProtocol_LiteralCapturedConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, fixture := range literalReaderFixtures() {
		for _, consumer := range forwardedConsumerFixtures() {
			t.Run(fixture.name+"/"+consumer.name, func(t *testing.T) {
				capture := "INPUT=`" + fixture.command + "`"
				require.NotEmpty(t, wakeLaunchProblems(source+"\n"+capture+"\n"+consumer.text))
			})
		}
	}
}

func TestWakeLeaseProtocol_CompoundLiteralCaptures_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, consumer := range forwardedConsumerFixtures() {
		t.Run(consumer.name, func(t *testing.T) {
			capture := "STAMP=`date +%s`; INPUT=`command -- mtix inbox --agent worker --format prompt`"
			require.NotEmpty(t, wakeLaunchProblems(source+"\n"+capture+"\n"+consumer.text))
		})
	}
}

func TestWakeLeaseProtocol_DollarFramedCapturedConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, consumer := range forwardedConsumerFixtures() {
		t.Run(consumer.name, func(t *testing.T) {
			capture := "INPUT=`command -- mtix inbox --agent \"$AGENT\" --format prompt`"
			require.NotEmpty(t, wakeLaunchProblems(source+"\n"+capture+"\n"+consumer.text))
		})
	}
}

func TestWakeLeaseProtocol_LiteralMetadataAndReferences_Accepted(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, fixture := range []struct{ name, text string }{
		{"Date", "STAMP=`date +%s`; report \"$STAMP\""},
		{"ForwardedDate", "STAMP=`command date +%s`; report \"$STAMP\""},
		{"Lease", "OUTCOME=`command -- mtix inbox lease acquire --agent worker`; printf '%s\\n' \"$OUTCOME\" >&2"},
		{"Release", "OUTCOME=`mtix inbox lease release --agent worker --holder \"$HOLDER\"`; printf '%s\\n' \"$OUTCOME\" >&2"},
	} {
		t.Run(fixture.name, func(t *testing.T) { require.Empty(t, wakeLaunchProblems(source+"\n"+fixture.text)) })
	}
	t.Run("DocumentReference", func(t *testing.T) {
		prose := "Read `mtix inbox --agent worker --format prompt` for the current queue."
		require.Empty(t, wakeLaunchProblems(prose))
		require.Equal(t, []string{"mtix inbox --agent worker --format prompt"}, wakeCommandLines(prose))
	})
	t.Run("SeparatedMetadata", func(t *testing.T) {
		require.Empty(t, wakeLaunchProblems(source+"\n"+strings.ReplaceAll("STAMP=`date +%s`; report \"$STAMP\"", "; ", "\n")))
	})
}
