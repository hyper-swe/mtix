// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"regexp"
	"strings"
)

var wakeCommandStartRE = regexp.MustCompile(`(?:^|[;&|({\n])\s*`)

// MTIX-112.26 treats an unknown command's inbox verb as a reader source.
// This inspects lexical command frames without resolving aliases or variables.
func wakeUnknownReaderSource(text string) bool {
	for _, start := range wakeCommandStartRE.FindAllStringIndex(text, -1) {
		command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text[start[1]:]), "#"))
		head, tail, ok := wakeActualCommandHead(command)
		for ok && strings.Contains(head, "=") {
			head, tail, ok = wakeActualCommandHead(tail)
		}
		if ok && head != ":" && wakeInboxAfterFlags(tail, wakeReaderAppearanceRE.MatchString(head+" ")) {
			return true
		}
	}
	return false
}
