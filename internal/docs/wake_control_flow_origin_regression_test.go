// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Control-flow extraction preserves capture identities and document references.
package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func controlFlowOriginFixtures() []struct{ name, prefix, suffix, variable string } {
	return []struct{ name, prefix, suffix, variable string }{
		{"Conditional", "if PARCEL=", "; then", "PARCEL"},
		{"NegatedConditional", "if ! COLLECTED=", "; then", "COLLECTED"},
		{"WhileCondition", "while DELIVERED=", "; do", "DELIVERED"},
		{"UntilCondition", "until QUEUED=", "; do", "QUEUED"},
		{"FollowingCommand", "true; BUNDLE=", "", "BUNDLE"},
		{"ConditionalBody", "if true; then FRESH=", "; fi", "FRESH"},
		{"GroupedAssignment", "{ PACKET=", "; }", "PACKET"},
	}
}

func TestWakeCommandLines_ControlFlowCaptures_PreservesCompleteOrigin(t *testing.T) {
	for _, frame := range controlFlowOriginFixtures() {
		for _, command := range []string{"mtix inbox --agent worker --format prompt", "command mtix inbox --agent worker --format prompt", "command -- mtix inbox --agent worker --format prompt"} {
			line := frame.prefix + "`" + command + "`" + frame.suffix
			t.Run(frame.name+"/"+strings.Fields(command)[0], func(t *testing.T) {
				assert.Contains(t, wakeCommandLines(line), line)
				assert.Contains(t, wakeCommandLines("# "+line), line)
			})
		}
	}
}

func TestWakeInputVariables_ControlFlowCaptures_PreservesSourceAndAliases(t *testing.T) {
	for _, frame := range controlFlowOriginFixtures() {
		t.Run(frame.name, func(t *testing.T) {
			line := frame.prefix + "`command -- mtix inbox --agent worker --format prompt`" + frame.suffix
			lines := wakeCommandLines(line + "\nEARLIER=\"$" + frame.variable + "\"\nLATER=\"$EARLIER\"")
			inputs := wakeInputVariables(lines)
			assert.True(t, inputs[frame.variable], "captured source identity")
			assert.True(t, inputs["EARLIER"], "first alias identity")
			assert.True(t, inputs["LATER"], "second alias identity")
		})
	}
}

func TestWakeLeaseProtocol_ControlFlowCapturedConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, frame := range controlFlowOriginFixtures() {
		for _, consumer := range []struct{ name, text string }{
			{"Report", "report \"$VALUE\""},
			{"VariableFormat", "printf \"$VALUE\""},
			{"DeferredReport", "trap 'report \"$VALUE\"' EXIT"},
			{"AliasDeferredReport", "EARLIER=\"$VALUE\"; LATER=\"$EARLIER\"; trap 'report \"$LATER\"' EXIT"},
		} {
			t.Run(frame.name+"/"+consumer.name, func(t *testing.T) {
				line := frame.prefix + "`command -- mtix inbox --agent worker --format prompt`" + frame.suffix
				use := strings.ReplaceAll(consumer.text, "$VALUE", "$"+frame.variable)
				assert.NotEmpty(t, wakeLaunchProblems(source+"\n"+line+"\n"+use))
			})
		}
	}
}

func TestWakeLeaseProtocol_ControlFlowOtherForwarding_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, command := range []string{"command -p mtix", "env mtix"} {
		t.Run(strings.Fields(command)[0], func(t *testing.T) {
			line := "if COLLECTED=`" + command + " inbox --agent worker --format prompt`; then"
			assert.NotEmpty(t, wakeLaunchProblems(source+"\n"+line+"\nreport \"$COLLECTED\"\nfi"))
		})
	}
}

func TestWakeLeaseProtocol_ControlFlowPairedReferences_RetainsControls(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, fixture := range []struct {
		name, text string
		refused    bool
	}{
		{"DollarConditional", "if PARCEL=`command -- mtix inbox --agent \"$AGENT\" --format prompt`; then\nreport \"$PARCEL\"\nfi", true},
		{"DirectCapture", "PARCEL=`mtix inbox --agent worker --format prompt`\nreport \"$PARCEL\"", true},
		{"BareConsumer", "report \"$PAYLOAD\"", true},
		{"DateMetadata", "STAMP=`command date +%s`; report \"$STAMP\"", false},
		{"LeaseMetadata", "OUTCOME=`command -- mtix inbox lease acquire --agent worker`; printf '%s\\n' \"$OUTCOME\" >&2", false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			problems := wakeLaunchProblems(source + "\n" + fixture.text)
			if fixture.refused {
				assert.NotEmpty(t, problems)
			} else {
				assert.Empty(t, problems)
			}
		})
	}
	t.Run("MarkdownReference", func(t *testing.T) {
		line := "Read `mtix inbox --agent worker --format prompt` for the current queue."
		require.Empty(t, wakeLaunchProblems(line))
		require.Equal(t, []string{"mtix inbox --agent worker --format prompt"}, wakeCommandLines(line))
	})
}
