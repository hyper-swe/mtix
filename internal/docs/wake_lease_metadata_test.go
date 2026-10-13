// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Metadata recognition checks command positions, including nested substitutions.
package docs

import (
	"regexp"
	"strings"
)

var wakeMetadataStartRE = regexp.MustCompile(`(?:^|\|\||[;|]|&&|\s&\s|\$\(|\bthen\b|\bdo\b|\belse\b)\s*(?:[({!]\s*)*([^\s;&|]+)`)
var wakeMetadataAssignmentRE = regexp.MustCompile(`^[A-Z_][A-Z_0-9]*=`)
var wakeMetadataFunctionRE = regexp.MustCompile(`^[a-z_][a-z_0-9]*\(\)$`)

// wakeStopFileLines are the only commands that touch the renewer stop marker
// (MTIX-163); everything else, including any other rm, is not metadata.
var wakeStopFileLineRE = regexp.MustCompile(`^(?:rm -f -- "\$STOP_FILE" 2>/dev/null \|\| :|\(set -C; : > "\$STOP_FILE"\) 2>/dev/null \|\| :)(?:;|$)`)

func wakeProtocolMetadataLine(line string) bool {
	if wakeStopFileLineRE.MatchString(strings.TrimSpace(line)) {
		return true
	}
	if strings.HasPrefix(line, "trap '") {
		body, _, found := strings.Cut(strings.TrimPrefix(line, "trap '"), "'")
		return found && wakeProtocolMetadataLine(body)
	}
	line = wakeMetadataArithmeticRE.ReplaceAllString(line, "0")
	// A case arm contributes framing, not an executable command.
	if before, after, found := strings.Cut(line, ") "); found && !strings.Contains(before, "$(") && !strings.Contains(before, " ") {
		line = after
	}
	if !wakeMetadataSubstitutions(line) {
		return false
	}
	line = wakeMetadataQuotedRE.ReplaceAllStringFunc(line, func(quoted string) string {
		if quoted == `"$MTIX_BIN"` {
			return quoted
		}
		return "word"
	})
	line = wakeMetadataFrameRE.ReplaceAllString(line, " ")
	return wakeMetadataCommandHeads(line)
}

func wakeMetadataCommandHeads(line string) bool {
	starts := wakeMetadataStartRE.FindAllStringSubmatchIndex(line, -1)
	if len(starts) == 0 {
		return false
	}
	for _, start := range starts {
		word, tail, ok := wakeActualCommandHead(line[start[2]:])
		if !ok {
			return false
		}
		if wakeMetadataAssignmentRE.MatchString(word) || wakeMetadataFunctionRE.MatchString(word) {
			continue
		}
		if word == "$MTIX_BIN" || word == "mtix" {
			if !wakeMetadataLeaseHeadRE.MatchString(tail) {
				return false
			}
			continue
		}
		if !wakeMetadataCommand(word) {
			return false
		}
	}
	return true
}

func wakeMetadataCommand(word string) bool {
	switch word {
	case "if", "while", "for", "case", "trap", "[", "then", "else", "fi", "do", "done", "esac", "break", "continue", "exit", "return", ":", "}":
		return true
	case "report", "resolve_tool", "stop_children", "stop_renewal", "stop_renewal_commands", "cleanup", "interrupted", "lost_lease", "renew_loop":
		return true
	case "kill", "wait", "sleep", "printf", "mkdir", "chmod", "date", "dirname", "basename", "cd", "pwd":
		return true
	}
	return false
}

var wakeMetadataQuotedRE = regexp.MustCompile(`"[^"\n]*"|'[^'\n]*'`)
var wakeMetadataSubstitutionRE = regexp.MustCompile(`\$\(\s*([^\s)]+)`)

func wakeMetadataSubstitutions(line string) bool {
	if !wakeMetadataBacktickSubstitutions(line) {
		return false
	}
	for _, match := range wakeMetadataSubstitutionRE.FindAllStringSubmatchIndex(line, -1) {
		word, tail, ok := wakeActualCommandHead(line[match[2]:])
		if !ok {
			return false
		}
		if (word == "$MTIX_BIN" || word == "mtix") && wakeMetadataLeaseHeadRE.MatchString(tail) {
			continue
		}
		if wakeMetadataAssignmentRE.MatchString(word) || wakeMetadataCommand(word) {
			continue
		}
		return false
	}
	return true
}

var wakeMetadataArithmeticRE = regexp.MustCompile(`\$\(\([A-Z_0-9 +*/%-]+\)\)`)

var wakeMetadataFrameRE = regexp.MustCompile(`\b(?:if|then|else|while|do)\s+`)

var wakeMetadataLeaseHeadRE = regexp.MustCompile(`^[ \t]+inbox[ \t]+lease[ \t]+(?:acquire|release)(?:[ \t;&|)]|$)`)

var wakeMetadataBacktickRE = regexp.MustCompile("`([^`\\n]*)`")

func wakeMetadataBacktickSubstitutions(line string) bool {
	captures := wakeMetadataBacktickRE.FindAllStringSubmatch(line, -1)
	if strings.Count(line, "`") != 2*len(captures) {
		return false
	}
	for _, capture := range captures {
		if !wakeProtocolMetadataLine(capture[1]) {
			return false
		}
	}
	return true
}
