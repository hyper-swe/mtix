// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWakeGuidance_CaseCommandResets(t *testing.T) {
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, tt := range []struct{ name, body string }{
		{"if", "if case b in b) echo %s;; esac; then :; fi"},
		{"then", "if true; then case b in b) echo %s;; esac; fi"},
		{"else", "if false; then :; else case b in b) echo %s;; esac; fi"},
		{"elif", "if false; then :; elif case b in b) echo %s;; esac; then :; fi"},
		{"while", "while case b in b) echo %s;; esac; do break; done"},
		{"until", "until case b in b) echo %s;; esac; do :; done"},
		{"time", "time case b in b) echo %s;; esac"},
		{"do", "for x in b; do case b in b) echo %s;; esac; done"},
		{"negation", "! { case b in (b) echo %s;; esac; }"},
		{"brace", "{ case b in (b) echo %s;; esac; }"},
		{"paren", "(case b in (b) echo %s;; esac)"},
		{"pipe", "echo a | case b in b) echo %s;; esac"},
		{"and", "true && case b in b) echo %s;; esac"},
		{"or", "false || case b in b) echo %s;; esac"},
		{"background", ": & case b in b) echo %s;; esac"},
		{"pipe_ampersand", "echo a |& case b in b) echo %s;; esac"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, output := range []struct {
				part, tail string
				accepted   bool
			}{{"in", "box", false}, {"da", "te", true}} {
				word := "$( " + fmt.Sprintf(tt.body, output.part) + ")" + output.tail
				require.Equal(t, []string{word, "suffix"}, wakeSpecialWords(word+" suffix"))
				command := "$M " + word
				assertWakeTokenPolicy(t, command, output.accepted)
				require.Equal(t, output.accepted, len(wakeLaunchProblems(script+"\n"+command)) == 0)
			}
		})
	}
}

func TestWakeGuidance_CaseReservedCommandPositions(t *testing.T) {
	for _, keyword := range []string{"if", "then", "else", "elif", "fi", "do", "done", "while", "until", "for", "select", "time", "function", "!", "esac"} {
		t.Run(keyword, func(t *testing.T) {
			scan := wakeCaseScan{command: true}
			scan.word(keyword)
			for _, word := range []string{"case", "b", "in", "b"} {
				scan.word(word)
			}
			require.True(t, scan.patternClose(), "case pattern after %s must not close the enclosing substitution", keyword)
			scan.word("echo")
			scan.word("esac")
			require.Equal(t, byte('b'), scan.phase(), "an ordinary argument must not end the case body")
		})
	}
}

func TestWakeGuidance_CaseClosingPositions(t *testing.T) {
	for _, phase := range []byte{'P', 'b'} {
		t.Run(string(phase), func(t *testing.T) {
			scan := wakeCaseScan{phases: []byte{phase}, command: true}
			scan.word("esac")
			for _, word := range []string{"case", "b", "in", "b"} {
				scan.word(word)
			}
			require.True(t, scan.patternClose(), "a completed case must retain the next command position")
		})
	}
}
