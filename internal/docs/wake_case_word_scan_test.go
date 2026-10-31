// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import "strings"

// Each case frame advances through subject, in, pattern start, pattern, body.
// Command position is separate: ordinary arguments cannot become keywords.
type wakeCaseScan struct {
	phases  []byte
	command bool
}

func (scan *wakeCaseScan) phase() byte {
	if len(scan.phases) == 0 {
		return 0
	}
	return scan.phases[len(scan.phases)-1]
}

func (scan *wakeCaseScan) caseWord(word string) bool {
	switch scan.phase() {
	case 's':
		scan.phases[len(scan.phases)-1] = 'i'
	case 'i':
		if word == "in" {
			scan.phases[len(scan.phases)-1] = 'P'
		}
	case 'P':
		if word == "esac" {
			scan.phases = scan.phases[:len(scan.phases)-1]
			scan.command = false
		} else {
			scan.phases[len(scan.phases)-1] = 'p'
		}
	case 'p':
	default:
		return false
	}
	return true
}

func (scan *wakeCaseScan) word(word string) {
	if scan.caseWord(word) || !scan.command {
		return
	}
	scan.command = false
	switch word {
	case "case":
		scan.phases = append(scan.phases, 's')
	case "esac":
		if len(scan.phases) > 0 {
			scan.phases = scan.phases[:len(scan.phases)-1]
		}
	case "if", "then", "elif", "else", "do", "while", "until", "!", "time":
		scan.command = true
	}
}

func (scan *wakeCaseScan) patternClose() bool {
	if scan.phase() != 'P' && scan.phase() != 'p' {
		return false
	}
	scan.phases[len(scan.phases)-1] = 'b'
	scan.command = true
	return true
}

func (scan *wakeCaseScan) operator(text string, start int) {
	if scan.phase() == 'b' && (strings.HasPrefix(text[start:], ";;") || strings.HasPrefix(text[start:], ";&")) {
		scan.phases[len(scan.phases)-1] = 'P'
	}
	if strings.ContainsRune(";|&\n", rune(text[start])) {
		scan.command = true
	}
}

func wakeParenthesizedWordEnd(text string, start int) int {
	scan := wakeCaseScan{command: true}
	for i := start + 1; i < len(text); i++ {
		c := text[i]
		if c == ')' {
			if !scan.patternClose() && len(scan.phases) == 0 {
				return i + 1
			}
		} else if c == '(' && (scan.phase() == 'P' || scan.phase() == 'p') {
			continue
		} else if strings.ContainsRune("({[", rune(c)) {
			end := wakeBalancedWordEnd(text, i)
			scan.word(text[i:end])
			i = end - 1
		} else if strings.ContainsRune(" \t\r\n;|&<>", rune(c)) {
			scan.operator(text, i)
		} else {
			end := wakeCaseWordEnd(text, i)
			scan.word(text[i:end])
			i = end - 1
		}
	}
	return len(text)
}

func wakeCaseWordEnd(text string, start int) int {
	for i := start; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) {
			i++
		} else if end := wakeLexicalGroupEnd(text, i); end > i {
			i = end - 1
		} else if text[i] == '{' || text[i] == '[' {
			i = wakeBalancedWordEnd(text, i) - 1
		} else if strings.ContainsRune(" \t\r\n;|&<>()", rune(text[i])) {
			return i
		}
	}
	return len(text)
}
