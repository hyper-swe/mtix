// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"regexp"
	"strings"
)

// Word runs are ordinary tokens, without interpretation. Dollar and glob markers
// retain command position; words without a word run receive an unknown marker.
var wakeLogicalTokenRE = regexp.MustCompile(`\$|[A-Za-z0-9_]+|\*`)
var wakeKnownReaderHeadRE = regexp.MustCompile(`^(?:(?:[A-Za-z_0-9./-]*/)?mtix|\$\{?MTIX_BIN\}?)$`)
var wakeMetadataRedirectRE = regexp.MustCompile(`(?:[ \t]+[012]?>{1,2}(?:/dev/null|"\$WAKE_AGENT_LOG"))+$`)
var wakeLeaseOccurrenceRE = regexp.MustCompile(`(?:^|[^$A-Za-z_0-9./-])(?:mtix|"?\$\{?MTIX_BIN\}?"?)[ \t]+inbox[ \t]+lease\b`)

// wakeStatusFormRE is the one approved non-reader inbox form beyond lease and ack
// (MTIX-112.40.6): mtix inbox status --agent <id> with only the documented
// --json and --limit flags, as a complete command. It reads wake state, never
// inbox events, so it needs no input provenance. Anything else stays a reader.
var wakeStatusFormRE = regexp.MustCompile(`^(?:[A-Za-z_0-9./-]*/)?mtix inbox status --agent (?:<agent>|[A-Za-z_0-9.-]+)(?: (?:--json|\[--json\]))?(?: (?:--limit [0-9]+|\[--limit N\]))?$`)

func wakeLogicalTokens(text string) []string {
	var tokens []string
	unquote := strings.NewReplacer("\\", "", "\"", "", "'", "", "`", "$")
	for _, word := range wakeSpecialWords(text) {
		wordTokens := wakeLogicalTokenRE.FindAllString(unquote.Replace(word), -1)
		if len(wordTokens) == 0 {
			// An unclassified word still occupies a position before the reader.
			wordTokens = []string{"$"}
		}
		tokens = append(tokens, wordTokens...)
	}
	if wakeSpecialReaderWord(text) {
		// Retain a command position for special words, including command heads.
		tokens = append(tokens, "$", "inbox")
	}
	return tokens
}

func wakeContainsInboxToken(text string) bool {
	for _, token := range wakeLogicalTokens(text) {
		if token == "inbox" {
			return true
		}
	}
	return false
}

// Only a complete known metadata command can avoid source provenance. Redirects
// are restricted to the documented lease log destinations, never a suffix command.
func wakeExactMetadataSource(text string) bool {
	if text == `ack: mtix inbox ack <seq> --agent <id> -C '<board>'` {
		return true
	}
	if wakeStatusFormRE.MatchString(strings.TrimSpace(text)) {
		return true
	}
	head, tail, ok := wakeActualCommandHead(text)
	if !ok || !wakeKnownReaderHeadRE.MatchString(head) {
		return false
	}
	tail = wakeMetadataRedirectRE.ReplaceAllString(tail, "")
	return wakeReaderReferenceRE.MatchString("mtix"+tail) &&
		wakeInboxAfterFlags(tail, false) && !wakeInboxAfterFlags(tail, true)
}

// A protocol allowance cannot hide a second, unknown inbox occurrence.
func wakeOnlyLeaseInbox(text string) bool {
	rest := wakeLeaseOccurrenceRE.ReplaceAllString(text, " known-lease ")
	for _, token := range wakeLogicalTokens(rest) {
		if token == "inbox" {
			return false
		}
	}
	return true
}

func wakeDefinitionReadsInput(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if wakeReaderSource(line) && (!wakeOnlyLeaseInbox(line) || !wakeProtocolMetadataLine(line)) {
			return true
		}
	}
	return false
}
