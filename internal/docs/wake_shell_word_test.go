// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import "strings"

// wakeShellWord preserves quoted and escaped option operands as single words.
// It removes lexical quoting only; variables and substitutions are not expanded.
func wakeShellWord(text string) (string, string) {
	text = strings.TrimLeft(text, " \t\r\n")
	var word strings.Builder
	quote, escaped := byte(0), false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if escaped {
			word.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				word.WriteByte(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if strings.ContainsRune(" \t\r\n;&|()", rune(c)) {
			return word.String(), text[i:]
		}
		word.WriteByte(c)
	}
	return word.String(), ""
}
