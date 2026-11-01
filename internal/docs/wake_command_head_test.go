// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Command classification resolves forwarding before assigning input or metadata roles.
package docs

import (
	"regexp"
	"strings"
)

var wakeCommandWordRE = regexp.MustCompile(`^\s*("[^"\n]*"|'[^'\n]*'|[^\s;&|()]+)`)
var wakeCaptureAssignmentRE = regexp.MustCompile("([A-Za-z_][A-Za-z_0-9]*)=[\"']?(\\$\\(|`)")

func wakeActualCommandHead(text string) (string, string, bool) {
	match := wakeCommandWordRE.FindStringSubmatchIndex(text)
	if match == nil {
		return "", "", false
	}
	word := strings.Trim(text[match[2]:match[3]], "\"'")
	tail := text[match[1]:]
	if word != "command" {
		return word, tail, true
	}
	tail = strings.TrimLeft(tail, " \t")
	if strings.HasPrefix(tail, "-- ") || strings.HasPrefix(tail, "--\t") {
		tail = strings.TrimLeft(tail[2:], " \t")
	}
	if strings.HasPrefix(tail, "-v ") || strings.HasPrefix(tail, "-V ") {
		operand := strings.TrimSpace(tail[3:])
		if !wakeMetadataLookupOperand(operand) {
			return "", "", false
		}
		return ":", "", true
	}
	match = wakeCommandWordRE.FindStringSubmatchIndex(tail)
	if match == nil {
		return "", "", false
	}
	word = strings.Trim(tail[match[2]:match[3]], "\"'")
	if strings.HasPrefix(word, "-") || word == "command" {
		return "", "", false
	}
	return word, tail[match[1]:], true
}

func wakeCaptureBody(text string) (string, bool) {
	if strings.HasPrefix(text, "`") {
		end := strings.Index(text[1:], "`")
		if end < 0 {
			return "", false
		}
		return text[1 : 1+end], true
	}
	if !strings.HasPrefix(text, "$(") {
		return "", false
	}
	depth := 1
	for i := 2; i < len(text); i++ {
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return text[2:i], true
			}
		}
	}
	return "", false
}

func wakeBodyReadsInput(body string) bool { return wakeReaderSource(body) }

var wakeLookupRemainderRE = regexp.MustCompile(`^\s*(?:[0-9]*[<>]+(?:/dev/null|&[0-9]+)\s*)*(?:[);&|]|$)`)

func wakeMetadataLookupOperand(text string) bool {
	match := wakeCommandWordRE.FindStringSubmatchIndex(text)
	return match != nil && wakeLookupRemainderRE.MatchString(text[match[1]:])
}
