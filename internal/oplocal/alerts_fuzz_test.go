// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Fuzz the bounded diagnostic decoder without filesystem or clock side effects.
package oplocal

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func FuzzAlertSegment_DecodeRecords(f *testing.F) {
	host := "11111111111111111111111111111111"
	row := alertInput()
	row.ID = "0123456789abcdef0123456789abcdef"
	row.HostID = host
	row.FirstAt = alertTime()
	row.LastAt = alertTime()
	row.Count = 1
	seed, err := encodeAlertSegment([]Alert{row})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(string(seed))
	f.Add("{}\n")
	f.Add("{\n")
	f.Add("\xff\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > maxRecordSize {
			return
		}
		rows, decodeErr := decodeAlertSegment([]byte(input), host)
		if decodeErr != nil {
			return
		}
		for _, row := range rows {
			require.NoError(t, validateStoredAlert(row, host))
		}
		encoded, encodeErr := encodeAlertSegment(rows)
		if encodeErr != nil {
			return
		} // Canonical encoding can expand a valid bounded input beyond the segment limit.
		again, roundTripErr := decodeAlertSegment(encoded, host)
		require.NoError(t, roundTripErr)
		require.Equal(t, len(rows), len(again))
		for i := range rows {
			require.Equal(t, rows[i].ID, again[i].ID)
			require.Equal(t, rows[i].Count, again[i].Count)
			require.True(t, rows[i].FirstAt.Equal(again[i].FirstAt))
			require.True(t, rows[i].LastAt.Equal(again[i].LastAt))
		}
	})
}
