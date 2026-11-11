// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// loadSamples parses a fixture from testdata.
func loadSamples(t *testing.T, name string) []Record {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })
	recs, err := ParseRegistry(f)
	require.NoError(t, err)
	return recs
}

func TestCheckRegistry_RecordedFixture_Passes(t *testing.T) {
	recs := loadSamples(t, "registry-pass.jsonl")
	require.Len(t, recs, 5)
	require.NoError(t, CheckRegistry(recs, 4242))
}

func TestCheckRegistry_ContractBreaks_ReturnErrRegistryContract(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		pid     int
		wantMsg string
	}{
		{"required key missing", "registry-missing-key.jsonl", 4242, "messagingSocketPath"},
		{"status repeats with a newer timestamp", "registry-no-alternation.jsonl", 4242, "alternat"},
		{"never observed busy", "registry-never-busy.jsonl", 4242, "busy"},
		{"pid is not the child", "registry-pass.jsonl", 9999, "pid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckRegistry(loadSamples(t, tt.fixture), tt.pid)
			require.ErrorIs(t, err, ErrRegistryContract)
			require.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

func TestCheckRegistry_NoSamples_ReturnsErrRegistryContract(t *testing.T) {
	require.ErrorIs(t, CheckRegistry(nil, 1), ErrRegistryContract)
}

func TestCheckRegistry_StatusOutsideIdleBusy_Rejected(t *testing.T) {
	line := `{"pid":1,"sessionId":"s","cwd":"/c","messagingSocketPath":"/s","status":"waiting","statusUpdatedAt":5}` + "\n"
	recs, err := ParseRegistry(strings.NewReader(line))
	require.NoError(t, err)
	err = CheckRegistry(recs, 1)
	require.ErrorIs(t, err, ErrRegistryContract)
	require.Contains(t, err.Error(), "waiting")
}

func TestCheckRegistry_TimestampGoesBackwards_Rejected(t *testing.T) {
	in := `{"pid":1,"sessionId":"s","cwd":"/c","messagingSocketPath":"/s","status":"idle","statusUpdatedAt":50}
{"pid":1,"sessionId":"s","cwd":"/c","messagingSocketPath":"/s","status":"busy","statusUpdatedAt":40}
`
	recs, err := ParseRegistry(strings.NewReader(in))
	require.NoError(t, err)
	err = CheckRegistry(recs, 1)
	require.ErrorIs(t, err, ErrRegistryContract)
	require.Contains(t, err.Error(), "backwards")
}

func TestCheckRegistry_StatusAbsentAfterWarmup_Rejected(t *testing.T) {
	in := `{"pid":1,"sessionId":"s","cwd":"/c","messagingSocketPath":"/s","status":"idle","statusUpdatedAt":50}
{"pid":1,"sessionId":"s","cwd":"/c","messagingSocketPath":"/s"}
`
	recs, err := ParseRegistry(strings.NewReader(in))
	require.NoError(t, err)
	err = CheckRegistry(recs, 1)
	require.ErrorIs(t, err, ErrRegistryContract)
	require.Contains(t, err.Error(), "status")
}

func TestParseRegistry_InvalidJSON_ReturnsErrMalformedRegistry(t *testing.T) {
	_, err := ParseRegistry(strings.NewReader("{not json}\n"))
	require.ErrorIs(t, err, ErrMalformedRegistry)
	require.Contains(t, err.Error(), "line 1")
}

func TestParseRegistry_NonObjectLine_ReturnsErrMalformedRegistry(t *testing.T) {
	_, err := ParseRegistry(strings.NewReader("[1,2]\n"))
	require.ErrorIs(t, err, ErrMalformedRegistry)
}

func TestCheckRegistry_EachRequiredKeyMissing_NamesThatKey(t *testing.T) {
	all := []string{"pid", "sessionId", "cwd", "messagingSocketPath"}
	require.ElementsMatch(t, all, requiredKeys(), "the pinned contract names exactly these keys")
	for _, key := range all {
		t.Run(key, func(t *testing.T) {
			raw := map[string]string{
				"pid": `"pid":4242`, "sessionId": `"sessionId":"s"`, "cwd": `"cwd":"/c"`,
				"messagingSocketPath": `"messagingSocketPath":"/s"`,
			}
			var parts []string
			for _, k := range all {
				if k != key {
					parts = append(parts, raw[k])
				}
			}
			line := "{" + strings.Join(parts, ",") + "}\n"
			recs, err := ParseRegistry(strings.NewReader(line))
			require.NoError(t, err)
			err = CheckRegistry(recs, 4242)
			require.ErrorIs(t, err, ErrRegistryContract)
			require.Contains(t, err.Error(), "required key "+key+" missing")
		})
	}
}

func TestCheckRegistry_OnlyIdleObserved_ReportsExactlyBusyNeverObserved(t *testing.T) {
	err := CheckRegistry(loadSamples(t, "registry-single-idle.jsonl"), 4242)
	require.ErrorIs(t, err, ErrRegistryContract)
	require.Equal(t, "registry contract broken: busy status never observed", err.Error())
}

func TestCheckRegistry_RecordedKeysAndStatusFixture_Passes(t *testing.T) {
	recs := loadSamples(t, "registry-recorded.jsonl")
	require.Len(t, recs, 15)
	require.NoError(t, CheckRegistry(recs, 4242))
	require.False(t, recs[0].Present["status"], "a real session starts without a status")
	for _, rec := range recs {
		require.True(t, rec.Present["peerProtocol"] && rec.Present["procStart"], "the recorded key set is carried whole")
	}
	for i := 1; i < len(recs); i++ {
		require.NotEqual(t, recs[i].Status, recs[i-1].Status, "recorded status alternates idle/busy (sample %d)", i+1)
	}
}

func TestCheckRegistry_RecordedFixtureWithOneKeyRemoved_IsRejected(t *testing.T) {
	recs := loadSamples(t, "registry-recorded.jsonl")
	delete(recs[4].Present, "messagingSocketPath")
	err := CheckRegistry(recs, 4242)
	require.ErrorIs(t, err, ErrRegistryContract)
	require.Contains(t, err.Error(), "sample 5: required key messagingSocketPath missing")
}
