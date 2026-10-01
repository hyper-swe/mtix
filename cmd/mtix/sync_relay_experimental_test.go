// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// TestSyncRelayHelp_MarksRelayExperimentalAndOptIn pins the experimental
// marker on the `mtix sync relay` group help (MTIX-95.48): the Short names it,
// and the Long says what experimental means and that nothing changes unless
// sync.relay.dir is set.
func TestSyncRelayHelp_MarksRelayExperimentalAndOptIn(t *testing.T) {
	cmd := newSyncRelayCmd()
	// Help text wraps; compare on whitespace-normalised text.
	long := strings.Join(strings.Fields(cmd.Long), " ")

	if !strings.Contains(cmd.Short, "EXPERIMENTAL") {
		t.Errorf("Short must carry the EXPERIMENTAL marker, got %q", cmd.Short)
	}
	for _, want := range []string{
		"experimental",
		"opt-in",
		"trial use",
		"on-disk format may change in 0.6.0",
		"not yet covered by the release gate",
		"sync.relay.dir",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("Long help missing %q:\n%s", want, cmd.Long)
		}
	}
}
