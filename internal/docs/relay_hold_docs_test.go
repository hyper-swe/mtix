// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A relay-carried hold is permanent and no command in this build clears it, so
// no shipped copy (template, plugin mirror, manual, docs) may pair a
// relay-carried hold with the create-and-cancel fix, which leaves the hold in
// place (the cancel is held too).
func TestShippedDocs_RelayHoldNamesNoRecreateFix(t *testing.T) {
	set := loadShippedDocs(t)
	seen := 0
	for path, text := range set.all {
		for _, p := range paragraphs(text) {
			if !strings.Contains(p.text, "relay-carried") || !strings.Contains(p.text, "held push events") {
				continue
			}
			seen++
			where := path + ":" + itoa(p.line)
			require.NotContains(t, p.text, "`mtix cancel`", "%s: a relay-carried hold must not be paired with mtix cancel", where)
			require.NotContains(t, p.text, "recreate it with `mtix create`", "%s: a relay-carried hold must not be paired with mtix create", where)
		}
	}
	require.NotZero(t, seen)
}
