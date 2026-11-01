// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"regexp"
	"strings"
)

var wakeNumericEscapeRE = regexp.MustCompile(`^\\(?:u[0-9A-Fa-f]{1,4}|U[0-9A-Fa-f]{1,8}|x[0-9A-Fa-f]{1,2}|[0-7]{1,3})`)

func wakeDecodeLiteralWord(word string) string {
	return wakeDecodeShellText(word, false)
}

// Quoted dollars cannot open ANSI-C strings. Substitutions have their own
// quote context, including when they occur inside double quotes.
func wakeDecodeShellText(text string, doubleQuoted bool) string {
	var decoded strings.Builder
	for i := 0; i < len(text); i++ {
		if group, end := wakeDecodedShellGroup(text, i, doubleQuoted); end > i {
			decoded.WriteString(group)
			i = end - 1
		} else if text[i] == '\\' && i+1 < len(text) {
			if escape := wakeNumericEscapeRE.FindString(text[i:]); escape != "" && !doubleQuoted {
				decoded.WriteString(wakeLiteralEscape(escape))
				i += len(escape) - 1
			} else {
				decoded.WriteString(text[i : i+2])
				i++
			}
		} else {
			decoded.WriteByte(text[i])
		}
	}
	return decoded.String()
}

func wakeDecodedShellGroup(text string, start int, doubleQuoted bool) (string, int) {
	if text[start] == '$' && start+1 < len(text) {
		next := text[start+1]
		if next == '(' || next == '{' {
			end := wakeBalancedWordEnd(text, start+1)
			if end <= start+2 {
				return text[start:end], end
			}
			return text[start:start+2] + wakeDecodeShellText(text[start+2:end-1], false) + text[end-1:end], end
		}
		if next == '\'' && !doubleQuoted {
			end := wakeQuotedWordEnd(text, start+1)
			if end <= start+2 {
				return text[start:end], end
			}
			literal := wakeLiteralEscapeRE.ReplaceAllStringFunc(text[start+2:end-1], wakeLiteralEscape)
			literal, _, _ = strings.Cut(literal, "\x00")
			return literal, end
		}
	}
	if text[start] == '`' || !doubleQuoted && strings.ContainsRune("'\"", rune(text[start])) {
		end := wakeQuotedWordEnd(text, start)
		if text[start] == '\'' || end <= start+1 {
			return text[start:end], end
		}
		return text[start:start+1] + wakeDecodeShellText(text[start+1:end-1], text[start] == '"') + text[end-1:end], end
	}
	return "", start
}
