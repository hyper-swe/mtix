// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Distinct mutation command paths receive the same safe completion guidance.
package main

import "testing"

func TestMutationBusyLock_AdditionalCobraAcknowledgements(t *testing.T) {
	for _, tc := range []mutationBusyCase{
		{"prompt", []string{"prompt", "TEST-1", "Updated prompt"}, nil},
		{"annotate", []string{"annotate", "TEST-1", "Progress recorded"}, nil},
		{"agent-state-set", []string{"agent", "state", "writer", "--set=working"}, func(t *testing.T) {
			registerTestAgent(t, "writer")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { checkMutationBusyCase(t, tc) })
	}
}
