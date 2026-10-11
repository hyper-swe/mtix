// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Input classification follows each capture and aliases across command contexts.
package docs

import "regexp"

var wakeInputAssignmentRE = regexp.MustCompile(`\b([A-Za-z_][A-Za-z_0-9]*)=("[^"\n]*"|'[^'\n]*'|[^\s;&|]+)`)

func wakeInputVariables(lines []string) map[string]bool {
	inputs := map[string]bool{"PAYLOAD": true}
	for _, line := range lines {
		for _, capture := range wakeSourceCaptures(line) {
			inputs[capture[1]] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, line := range lines {
			for _, assignment := range wakeInputAssignmentRE.FindAllStringSubmatch(line, -1) {
				if !inputs[assignment[1]] && wakeReferencesInput(assignment[2], inputs) {
					inputs[assignment[1]] = true
					changed = true
				}
			}
		}
	}
	return inputs
}

var wakeBacktickSourceRE = regexp.MustCompile("([A-Za-z_][A-Za-z_0-9]*)=[\"']?`\\s*(?:mtix|\"\\$MTIX_BIN\") inbox[^`\\n]*--format prompt[^`\\n]*`")

func wakeSourceCaptures(line string) [][]string {
	captures := wakeSourceVariableRE.FindAllStringSubmatch(line, -1)
	return append(captures, wakeBacktickSourceRE.FindAllStringSubmatch(line, -1)...)
}
