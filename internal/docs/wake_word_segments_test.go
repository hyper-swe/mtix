// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import "strings"

// Literal nonletters separate segments. Expansion bodies retain their letters
// together, without interpreting values, commands, alternatives or glob matches.
func wakeWordSegments(word string) []string {
	word = wakeDecodeLiteralWord(word)
	var text strings.Builder
	for i := 0; i < len(word); i++ {
		if end := wakeWordGroupEnd(word, i); end > i {
			for _, c := range word[i:end] {
				if wakeASCIILetter(c) {
					text.WriteRune(c)
				}
			}
			i = end - 1
		} else if wakeASCIILetter(rune(word[i])) {
			text.WriteByte(word[i])
		} else if !strings.ContainsRune("'\"\\$`{}()*?[]", rune(word[i])) {
			text.WriteByte(' ')
		}
	}
	return strings.Fields(text.String())
}

func wakeASCIILetter(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// Locate lexical substitution and expansion groups, including bare parameters.
func wakeWordGroupEnd(text string, start int) int {
	if text[start] == '`' {
		return wakeQuotedWordEnd(text, start)
	}
	if text[start] == '{' || text[start] == '[' {
		return wakeBalancedWordEnd(text, start)
	}
	if text[start] != '$' || start+1 >= len(text) {
		return start
	}
	next := text[start+1]
	if next == '(' || next == '{' {
		return wakeBalancedWordEnd(text, start+1)
	}
	if wakeASCIILetter(rune(next)) || next == '_' {
		end := start + 2
		for end < len(text) && (wakeASCIILetter(rune(text[end])) || strings.ContainsRune("_0123456789", rune(text[end]))) {
			end++
		}
		return end
	}
	if strings.ContainsRune("0123456789@*#?$!-", rune(next)) {
		return start + 2
	}
	return start
}

func wakeBalancedWordEnd(text string, start int) int {
	if text[start] == '(' && (start == 0 || !strings.ContainsRune("?*+@!", rune(text[start-1]))) {
		return wakeParenthesizedWordEnd(text, start)
	}
	closer := ")}]"[strings.IndexByte("({[", text[start])]
	for i := start + 1; i < len(text); i++ {
		c := text[i]
		if c == '\\' {
			i++
		} else if strings.ContainsRune("'\"`", rune(c)) {
			i = wakeQuotedWordEnd(text, i) - 1
		} else if strings.ContainsRune("({[", rune(c)) {
			i = wakeBalancedWordEnd(text, i) - 1
		} else if c == closer {
			return i + 1
		}
	}
	return len(text)
}

func wakeQuotedWordEnd(text string, start int) int {
	quote := text[start]
	ansi := quote == '\'' && start > 0 && text[start-1] == '$'
	for i := start + 1; i < len(text); i++ {
		if text[i] == '\\' && (quote != '\'' || ansi) {
			i++
		} else if text[i] == quote {
			return i + 1
		} else if quote == '"' && text[i] == '$' && i+1 < len(text) && strings.ContainsRune("({", rune(text[i+1])) {
			i = wakeBalancedWordEnd(text, i+1) - 1
		}
	}
	return len(text)
}
