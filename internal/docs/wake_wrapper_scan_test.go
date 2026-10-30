// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"regexp"
	"strconv"
	"strings"
)

// Word runs are ordinary tokens, without interpretation. Dollar and glob markers
// retain a preceding command position even when no literal word names it.
var wakeLogicalTokenRE = regexp.MustCompile(`\$|[A-Za-z0-9_]+|\*`)
var wakeKnownReaderHeadRE = regexp.MustCompile(`^(?:(?:[A-Za-z_0-9./-]*/)?mtix|\$\{?MTIX_BIN\}?)$`)
var wakeMetadataRedirectRE = regexp.MustCompile(`(?:[ \t]+[012]?>{1,2}(?:/dev/null|"\$WAKE_AGENT_LOG"))+$`)
var wakeLeaseOccurrenceRE = regexp.MustCompile(`(?:^|[^$A-Za-z_0-9./-])(?:mtix|"?\$\{?MTIX_BIN\}?"?)[ \t]+inbox[ \t]+lease\b`)

// wakeStatusFormRE is the one approved non-reader inbox form beyond lease and ack
// (MTIX-112.40.6): mtix inbox status --agent <id> with only the documented
// --json and --limit flags, as a complete command. It reads wake state, never
// inbox events, so it needs no input provenance. Anything else stays a reader.
var wakeStatusFormRE = regexp.MustCompile(`^(?:[A-Za-z_0-9./-]*/)?mtix inbox status --agent (?:<agent>|[A-Za-z_0-9.-]+)(?: (?:--json|\[--json\]))?(?: (?:--limit [0-9]+|\[--limit N\]))?$`)

// Retain the original token view as well as the lexical splice view: removing
// an expansion must not hide a reader word that occurs inside that expansion.
var wakeWordExpansionRE = regexp.MustCompile(`([A-Za-z0-9_])(?:\$\{[^{}]*\}|\$\(\))+([A-Za-z0-9_])`)
var wakeQuotedWordRE = regexp.MustCompile(`\$'(?:\\.|[^'\\])*'`)

func wakeLogicalTokens(text string) []string {
	normalize := strings.NewReplacer("\\", "", "\"", "", "'", "", "`", "$")
	original := normalize.Replace(text)
	tokens := wakeLogicalTokenRE.FindAllString(original, -1)
	joined := normalize.Replace(wakeQuotedWordRE.ReplaceAllStringFunc(text, wakeQuotedWord))
	for {
		next := wakeWordExpansionRE.ReplaceAllString(joined, "${1}${2}")
		if next == joined {
			break
		}
		joined = next
	}
	if joined != original {
		tokens = append(tokens, wakeLogicalTokenRE.FindAllString(joined, -1)...)
	}
	return tokens
}

// Decode literal quoted characters only; this does not evaluate shell values.
func wakeQuotedWord(text string) string {
	value := text[2 : len(text)-1]
	var word strings.Builder
	for value != "" {
		letter, _, tail, err := strconv.UnquoteChar(value, '\'')
		if err != nil {
			return text
		}
		word.WriteRune(letter)
		value = tail
	}
	return word.String()
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
