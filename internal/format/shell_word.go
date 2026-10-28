// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ShellWord renders value as one POSIX shell word for a command line that mtix
// prints for a person or agent to copy. Plain words (letters, digits and
// ._/:@%+,- not starting with - or =) are returned as they are; everything else
// is single-quoted with ' written as '\”. The second result is false when the
// value cannot be shown as a typeable command: empty, invalid UTF-8, or holding
// any rune that is not printable (NUL, controls, newline, tab, format and bidi
// marks, non-ASCII spaces). The caller must then print a handle or an inspect
// instruction instead of a command (MTIX-187.7.6).
func ShellWord(value string) (string, bool) {
	if value == "" || !utf8.ValidString(value) {
		return "", false
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return "", false
		}
	}
	if plainShellWord(value) {
		return value, true
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'", true
}

// ShellOperand renders value as a command operand: like ShellWord, and a value
// that starts with a dash is preceded by -- so it cannot be read as a flag.
func ShellOperand(value string) (string, bool) {
	word, ok := ShellWord(value)
	if ok && value[0] == '-' {
		return "-- " + word, true
	}
	return word, ok
}

func plainShellWord(value string) bool {
	if value[0] == '-' || value[0] == '=' {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._/:@%+,-", r):
		default:
			return false
		}
	}
	return true
}
