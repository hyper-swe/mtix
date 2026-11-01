// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

// MTIX-112.26 scans a logical executable line, not a shell grammar. Every token
// before inbox is conservative: groups, reserved words and unknown wrappers do
// not hide it. This deliberately refuses literal data such as echo inbox too.
// Ordinary prose is outside this scan; whole-line allowances are checked later.
func wakeUnknownReaderSource(text string) bool {
	for index, token := range wakeLogicalTokens(text) {
		if index > 0 && token == "inbox" {
			return true
		}
	}
	return false
}
