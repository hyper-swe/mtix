// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Case-arm extraction retains reader identities and independent metadata controls.
package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func caseArmOriginFixtures() []struct{ name, prefix, variable string } {
	return []struct{ name, prefix, variable string }{
		{"PlainAssignment", "literal) FREIGHT=", "FREIGHT"},
		{"NegatedAssignment", "literal) ! STOCK=", "STOCK"},
	}
}

func TestWakeCommandLines_CaseArmCaptures_PreservesCompleteOrigin(t *testing.T) {
	for _, frame := range caseArmOriginFixtures() {
		for _, reader := range []struct{ name, command string }{
			{"DirectReader", "mtix inbox --agent worker --format prompt"},
			{"ForwardedReader", "command mtix inbox --agent worker --format prompt"},
			{"SeparatedForwarding", "command -- mtix inbox --agent worker --format prompt"},
		} {
			t.Run(frame.name+"/"+reader.name, func(t *testing.T) {
				line := frame.prefix + "`" + reader.command + "`;;"
				assert.Contains(t, wakeCommandLines("case literal in\n"+line+"\nesac"), line)
				assert.Contains(t, wakeCommandLines("# "+line), line)
			})
		}
	}
}

func TestWakeInputVariables_CaseArmCaptures_PreservesSourceAndAliases(t *testing.T) {
	for _, frame := range caseArmOriginFixtures() {
		t.Run(frame.name, func(t *testing.T) {
			line := frame.prefix + "`command -- mtix inbox --agent worker --format prompt`;;"
			text := "case literal in\n" + line + "\nesac\nEARLIER=\"$" + frame.variable + "\"\nLATER=\"$EARLIER\""
			inputs := wakeInputVariables(wakeCommandLines(text))
			assert.True(t, inputs[frame.variable], "captured source identity")
			assert.True(t, inputs["EARLIER"], "first alias identity")
			assert.True(t, inputs["LATER"], "second alias identity")
		})
	}
}

func TestWakeLeaseProtocol_CaseArmCapturedConsumers_Refused(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, frame := range caseArmOriginFixtures() {
		for _, consumer := range []struct{ name, text string }{
			{"Report", "report \"$VALUE\""},
			{"VariableFormat", "printf \"$VALUE\""},
			{"DeferredReport", "trap 'report \"$VALUE\"' EXIT"},
			{"AliasDeferredReport", "EARLIER=\"$VALUE\"; LATER=\"$EARLIER\"; trap 'report \"$LATER\"' EXIT"},
		} {
			t.Run(frame.name+"/"+consumer.name, func(t *testing.T) {
				line := frame.prefix + "`command -- mtix inbox --agent worker --format prompt`;;"
				use := strings.ReplaceAll(consumer.text, "$VALUE", "$"+frame.variable)
				assert.NotEmpty(t, wakeLaunchProblems(source+"\ncase literal in\n"+line+"\nesac\n"+use))
			})
		}
	}
}

func TestWakeLeaseProtocol_CaseArmPairedReferences_RetainsControls(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, fixture := range []struct {
		name, text string
		refused    bool
	}{
		{"DollarFramedReader", "case literal in\nliteral) FREIGHT=`command -- mtix inbox --agent \"$AGENT\" --format prompt`;;\nesac\nreport \"$FREIGHT\"", true},
		{"DirectCapture", "FREIGHT=`mtix inbox --agent worker --format prompt`\nreport \"$FREIGHT\"", true},
		{"BareConsumer", "report \"$PAYLOAD\"", true},
		{"DateMetadata", "case literal in\nliteral) STAMP=`date +%s`;;\nesac\nreport \"$STAMP\"", false},
		{"LeaseMetadata", "case literal in\nliteral) OUTCOME=`command -- mtix inbox lease acquire --agent worker`;;\nesac\nprintf '%s\\n' \"$OUTCOME\" >&2", false},
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
		text := "The `literal` branch reads `mtix inbox --agent worker --format prompt` for the current queue."
		assert.Empty(t, wakeLaunchProblems(text))
		assert.Equal(t, []string{"literal", "mtix inbox --agent worker --format prompt"}, wakeCommandLines(text))
	})
}
