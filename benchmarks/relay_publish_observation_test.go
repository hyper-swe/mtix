// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package benchmarks

import (
	"encoding/json"
	"testing"
)

func logRelayPublishPair(t *testing.T, index int, pair relayPublishPair) {
	t.Helper()
	row := struct {
		Pair           int   `json:"pair"`
		ReferenceFirst bool  `json:"reference_first"`
		ReferenceCount int   `json:"reference_count"`
		CandidateCount int   `json:"candidate_count"`
		ReferenceNS    int64 `json:"reference_ns"`
		CandidateNS    int64 `json:"candidate_ns"`
	}{index, pair.ReferenceFirst, pair.Reference.Count, pair.Candidate.Count, int64(pair.Reference.Elapsed), int64(pair.Candidate.Elapsed)}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("relay_publish_pair %s", encoded)
}
