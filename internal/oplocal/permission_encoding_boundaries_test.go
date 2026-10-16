// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These exercise encoding boundaries directly: invalid Go values cannot be
// represented in a persisted JSON fixture, so a reader test cannot reach them.
func TestEncodeConfirmationsAndReplays_InvalidValues_ReturnsErrorWithoutPartialOutput(t *testing.T) {
	b := permissionFixtureBatch()
	b.ID = replayIntent().ID
	b.Time = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	row := Confirmation{Time: b.Time}
	for name, encode := range map[string]func() ([]byte, error){
		"confirmation": func() ([]byte, error) { return encodeConfirmations([]Confirmation{row}) },
		"replay":       func() ([]byte, error) { return encodePermissionReplays([]permissionReplay{{Batch: b}}, "host") },
	} {
		t.Run(name, func(t *testing.T) {
			data, err := encode()
			require.Error(t, err)
			require.Nil(t, data, "failed encodings must not escape as usable records")
			var marshalErr *json.MarshalerError
			require.ErrorAs(t, err, &marshalErr)
		})
	}
}

func TestPermissionGrantProjection_EncodingFailure_PreservesInputs(t *testing.T) {
	for _, kind := range []string{"grant-time", "inherited-json", "size"} {
		t.Run(kind, func(t *testing.T) {
			data := map[string]json.RawMessage{"future": json.RawMessage(`true`)}
			grants := []AgentGrant{{AgentID: "unrelated"}}
			switch kind {
			case "grant-time":
				grants[0].ConfirmedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			case "inherited-json":
				data["future"] = json.RawMessage(`{broken`)
			case "size":
				data["future"] = json.RawMessage(`"` + strings.Repeat("x", maxRecordSize) + `"`)
			}
			before := string(data["future"])
			result, err := permissionGrantProjection(data, grants, permissionFixtureBatch(), "host")
			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, before, string(data["future"]))
			require.Len(t, data, 1)
			require.Len(t, grants, 1)
			require.Equal(t, "unrelated", grants[0].AgentID)
		})
	}
}

func TestCommitPermissionRecord_UnsupportedOrOversizedValue_RefusesWithoutWriting(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.withPermissions(t.Context(), func(dir *os.File, host string) error {
		for _, value := range []any{func() {}, strings.Repeat("x", maxRecordSize)} {
			before := alertStateSnapshot(t, s)
			require.Error(t, s.commitPermissionRecord(dir, "synthetic-encoder.json", host, value))
			require.Equal(t, before, alertStateSnapshot(t, s))
		}
		return nil
	}))
}

func TestValidatePermissionBefore_InvalidCommitTime_RefusesEncoding(t *testing.T) {
	grants := []AgentGrant{{ConfirmedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	b := permissionFixtureBatch()
	p := permissionPending{Batch: &b, BeforeGrants: &grants}
	require.Error(t, validatePermissionBefore(p, "host"))
	p.Batch = nil
	require.ErrorContains(t, validatePermissionBefore(p, "host"), "has no batch")
}
