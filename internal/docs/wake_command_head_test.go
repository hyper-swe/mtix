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
var wakeInboxHeadRE = regexp.MustCompile(`^[ \t]+inbox(?:[ \t;&|)]|$)`)

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

func wakeBodyReadsInput(body string) bool {
	for _, start := range wakeMetadataStartRE.FindAllStringSubmatchIndex(body, -1) {
		word, tail, ok := wakeInputCommandHead(body[start[2]:])
		fields := strings.Fields(tail)
		reader := word == "$MTIX_BIN" || word == "${MTIX_BIN}" || word == "mtix" || strings.HasSuffix(word, "/mtix")
		if ok && reader && wakeInboxHeadRE.MatchString(tail) && (len(fields) < 2 || fields[1] != "lease") {
			return true
		}
	}
	return false
}

// Source recognition follows command forwarding without granting metadata status.
func wakeInputCommandHead(text string) (string, string, bool) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "command -p ") {
		text = strings.TrimSpace(strings.TrimPrefix(text, "command -p "))
	}
	word, tail, ok := wakeActualCommandHead(text)
	if !ok || word != "env" {
		return word, tail, ok
	}
	for {
		word, tail, ok = wakeActualCommandHead(strings.TrimSpace(tail))
		if !ok {
			return word, tail, false
		}
		if word == "-u" || word == "--unset" || word == "-C" || word == "--chdir" {
			operand := wakeCommandWordRE.FindStringIndex(tail)
			if operand == nil {
				return "", "", false
			}
			tail = tail[operand[1]:]
			continue
		}
		if strings.HasPrefix(word, "-") || strings.Contains(word, "=") {
			continue
		}
		return word, tail, true
	}
}

var wakeLookupRemainderRE = regexp.MustCompile(`^\s*(?:[0-9]*[<>]+(?:/dev/null|&[0-9]+)\s*)*(?:[);&|]|$)`)

func wakeMetadataLookupOperand(text string) bool {
	match := wakeCommandWordRE.FindStringSubmatchIndex(text)
	return match != nil && wakeLookupRemainderRE.MatchString(text[match[1]:])
}
