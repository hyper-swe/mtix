// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"strconv"
	"strings"
)

// neutralize drops every character the agent-text rule rejects (mapAgentText; tab and newline stay)
// and defuses the envelope markers in quoted text.
func neutralize(value string) string {
	value = mapAgentText(value, -1)
	return strings.NewReplacer(BeginMarker, "[quoted begin marker]", EndMarker, "[quoted end marker]", Preamble, "[quoted provenance line]", "reply:", "reply (quoted):", "ack:", "ack (quoted):").Replace(value)
}

func quoteMetadata(value string) string { return strconv.QuoteToASCII(neutralize(value)) }

func quoteBody(value string, limit int) (string, bool, bool) {
	value = neutralize(value)
	if limit == 0 {
		return strconv.QuoteToASCII(value), false, true
	}
	if limit < 2 {
		return "", false, false
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		encoded := strconv.QuoteToASCII(string(r))
		encoded = encoded[1 : len(encoded)-1]
		if b.Len()+len(encoded)+1 > limit {
			b.WriteByte('"')
			return b.String(), true, true
		}
		b.WriteString(encoded)
	}
	b.WriteByte('"')
	return b.String(), false, true
}
