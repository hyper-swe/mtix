// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"regexp"
	"strconv"
	"strings"
)

var wakeLiteralEscapeRE = regexp.MustCompile(`\\(?:u[0-9A-Fa-f]{1,4}|U[0-9A-Fa-f]{1,8}|x[0-9A-Fa-f]{1,2}|[0-7]{1,3}|c[ -~]|[abeEfnrtv\\'"?])`)

var wakeWordEscapeRE = regexp.MustCompile(`\$'(?:\\.|[^'\\])*'|\\(?:u[0-9A-Fa-f]{1,4}|U[0-9A-Fa-f]{1,8}|x[0-9A-Fa-f]{1,2}|[0-7]{1,3})`)

func wakeDecodeLiteralWord(word string) string {
	return wakeWordEscapeRE.ReplaceAllStringFunc(word, func(escaped string) string {
		if strings.HasPrefix(escaped, "$'") {
			literal := wakeLiteralEscapeRE.ReplaceAllStringFunc(escaped[2:len(escaped)-1], wakeLiteralEscape)
			literal, _, _ = strings.Cut(literal, "\x00")
			return literal
		}
		return wakeLiteralEscape(escaped)
	})
}

// This is a conservative lexical class, not expansion or command evaluation.
func wakeSpecialReaderWord(text string) bool {
	for _, word := range wakeSpecialWords(text) {
		if !strings.ContainsAny(word, "$`\\{}()'\"[]*?~@") {
			continue
		}
		for _, segment := range wakeWordSegments(word) {
			next := 0
			for _, letter := range strings.ToLower(segment) {
				if letter == rune("inbox"[next]) {
					next++
					if next == len("inbox") {
						return true
					}
				}
			}
		}
	}
	return false
}

// Quotes and substitution bodies stay in one word; unquoted shell operators
// and whitespace separate words. Their content is retained without evaluation.
func wakeSpecialWords(text string) []string {
	var words []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) {
			i++
			continue
		}
		if end := wakeLexicalGroupEnd(text, i); end > i {
			i = end - 1
			continue
		}
		if strings.ContainsRune(" \t\r\n;&|<>()", rune(text[i])) {
			if i > start {
				words = append(words, text[start:i])
			}
			start = i + 1
		}
	}
	if start < len(text) {
		words = append(words, text[start:])
	}
	return words
}

func wakeLexicalGroupEnd(text string, start int) int {
	if strings.ContainsRune("'\"`", rune(text[start])) {
		return wakeQuotedWordEnd(text, start)
	}
	if start+1 < len(text) {
		if text[start] == '$' && strings.ContainsRune("({", rune(text[start+1])) {
			return wakeBalancedWordEnd(text, start+1)
		}
		if strings.ContainsRune("?*+@!", rune(text[start])) && text[start+1] == '(' {
			return wakeBalancedWordEnd(text, start+1)
		}
	}
	return start
}

func wakeLiteralEscape(text string) string {
	if index := strings.IndexByte("abeEfnrtv\\'\"?", text[1]); index >= 0 {
		return string("\a\b\x1b\x1b\f\n\r\t\v\\'\"?"[index])
	}
	if text[1] == 'c' {
		if text[2] == '?' {
			return "\x7f"
		}
		return string(text[2] & 0x1f)
	}
	base, digits := 8, text[1:]
	if text[1] == 'x' || text[1] == 'u' || text[1] == 'U' {
		base, digits = 16, text[2:]
	}
	value, err := strconv.ParseUint(digits, base, 32)
	if err != nil {
		return text
	}
	return string(rune(value))
}
