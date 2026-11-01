// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Reader form tests pair executable sources with unrelated shell controls.
package docs

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func wakeDocumentFile(path, text string) bool {
	if strings.HasPrefix(strings.TrimSpace(text), "#!") {
		return false
	}
	return strings.HasSuffix(strings.ToLower(path), ".md") || strings.HasSuffix(strings.ToLower(path), ".tmpl")
}

func TestWakeGuidance_RawExampleKinds_Checked(t *testing.T) {
	for _, path := range []string{"examples/hooks/pre-push", "examples/hooks/pre-receive", "examples/hooks/github-action.yml"} {
		t.Run(path, func(t *testing.T) {
			source := wakeSource(t, path)
			require.Empty(t, wakeLaunchProblems(source, wakeDocumentFile(path, source)))
			changed := source + "\n" + `FRESH="$(mtix inbox --agent worker --format prompt)"` + "\n" + `next-runtime -p "$FRESH"`
			require.NotEmpty(t, wakeLaunchProblems(changed, wakeDocumentFile(path, changed)))
		})
	}
}

func TestWakeGuidance_ReaderWrappers_Checked(t *testing.T) {
	sources := []string{
		`mtix -C board inbox --agent w --format prompt`,
		`mtix --project-dir board inbox --agent w --format prompt`,
		`mtix --project-dir=board --json inbox --agent w`,
		`mtix --log-level info -C "board path" inbox --format prompt`,
		`timeout 10 mtix inbox --format prompt`, `nice -n 5 mtix inbox --format prompt`,
		`exec mtix inbox --format prompt`, `sh -c 'mtix inbox --format prompt'`,
		`env -u OTHER mtix inbox --format prompt`, `env --unset OTHER mtix inbox --format prompt`,
		`env -C board mtix inbox --format prompt`, `env --chdir board mtix inbox --format prompt`,
	}
	for i, source := range sources {
		unrelated := strings.Replace(source, "mtix inbox --format prompt", "date -u", 1)
		if strings.HasPrefix(source, "mtix ") {
			unrelated = "date -u"
		}
		for j, frame := range []string{`FRESH="$(%s)"`, `next-runtime "$(%s)"`, `read FRESH < <(%s)`, `mapfile -t FRESH < <(%s)`} {
			t.Run(fmt.Sprintf("%d/%d", i, j), func(t *testing.T) {
				sourceText := fmt.Sprintf(frame, source) + "\n" + `next-runtime -p "$FRESH"`
				require.NotEmpty(t, wakeLaunchProblems("```sh\n"+sourceText+"\n```", true))
				control := fmt.Sprintf(frame, unrelated) + "\n" + `next-runtime -p "$FRESH"`
				require.Empty(t, wakeLaunchProblems("```sh\n"+control+"\n```", true))
				require.Empty(t, wakeLaunchProblems(sourceText, true), "unmarked prose")
			})
		}
	}
}

func TestWakeGuidance_ReaderDefinitions_Checked(t *testing.T) {
	for _, definition := range []string{
		`alias fetch='mtix inbox --format prompt'`,
		`fetch() { mtix inbox --format prompt; }`,
		"fetch() {\nmtix inbox --format prompt\n}",
		"function fetch {\nprintf '%s' 'brace }'\nmtix inbox --format prompt\n}",
		"fetch() {\n# brace }\nmtix inbox --format prompt\n}",
		"# fetch() {\n# mtix inbox --format prompt\n# }",
		"fetch()\n{\nmtix inbox --format prompt\n}",
		`fetch() {
printf '%s' "escaped \" }"
mtix inbox --format prompt
}`,
	} {
		t.Run(definition, func(t *testing.T) {
			suffix := "\n" + `FRESH="$(fetch)"` + "\n" + `next-runtime -p "$FRESH"`
			require.NotEmpty(t, wakeLaunchProblems("```sh\n"+definition+suffix+"\n```", true))
			control := strings.ReplaceAll(definition, "mtix inbox --format prompt", "date -u")
			require.Empty(t, wakeLaunchProblems("```sh\n"+control+suffix+"\n```", true))
		})
	}
}

func TestWakeGuidance_FileKinds_RespectShellContent(t *testing.T) {
	tests := []struct {
		path, text string
		document   bool
	}{
		{"README.md", "Ordinary prose", true}, {"skill.md.tmpl", "Ordinary prose", true},
		{"pre-push", "#!/bin/sh\necho ready", false}, {"action.yml", "run: echo ready", false},
		{"renamed.md", "#!/bin/sh\necho ready", false}, {"wake.sh", "echo ready", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) { require.Equal(t, tt.document, wakeDocumentFile(tt.path, tt.text)) })
	}
}

func TestWakeGuidance_ConservativeConsumers_Checked(t *testing.T) {
	for _, command := range []string{`next-runtime --message "$UNDECLARED"`, `eval "$UNDECLARED"`, `printf '%s\n' "$UNDECLARED" | xargs next-runtime`} {
		t.Run(command, func(t *testing.T) { require.NotEmpty(t, wakeLaunchProblems(command)) })
	}
	for _, command := range []string{`git add -- "$UNDECLARED"`, `printf '%s\n' "$UNDECLARED" | sort`, `NOW=$(date -u)`} {
		t.Run(command, func(t *testing.T) { require.Empty(t, wakeLaunchProblems(command)) })
	}
}
