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
// It deliberately refuses unrelated executable text such as echo inbox too;
// ordinary document prose is outside this scan.
func wakeUnknownReaderSource(text string) bool {
	for _, start := range wakeCommandStartRE.FindAllStringIndex(text, -1) {
		command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text[start[1]:]), "#"))
		if wakeUnwrappedReader(command) {
			return true
		}
	}
	return false
}
