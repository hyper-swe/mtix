// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests bounded retained projections independently of durable hourly rate state.
package oplocal

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fullAlertSegment(t *testing.T, host string, target *Alert) []byte {
	t.Helper()
	rows := make([]Alert, 300)
	for i := range rows {
		rows[i] = Alert{ID: fmt.Sprintf("%032x", i+100), HostID: host, Agent: fmt.Sprintf("fill-%d", i), Class: AlertClassWakeBackoff, Count: 1, FirstAt: alertTime(), LastAt: alertTime(), Detail: strings.Repeat("x", 3200)}
	}
	if target != nil {
		rows[0] = *target
	}
	data, err := encodeAlertSegment(rows)
	require.NoError(t, err)
	remaining := maxRecordSize - len(data)
	for i := range rows {
		if i == 0 && target != nil {
			continue
		}
		n := min(4096-len(rows[i].Detail)-len(rows[i].Fix), remaining)
		rows[i].Detail += strings.Repeat("x", n)
		remaining -= n
	}
	require.Zero(t, remaining)
	data, err = encodeAlertSegment(rows)
	require.NoError(t, err)
	require.Len(t, data, maxRecordSize)
	return data
}

func seedAlertSegment(t *testing.T, s *State, index int, data []byte) {
	t.Helper()
	dir, err := s.directory(true)
	require.NoError(t, err)
	require.NoError(t, writeData(dir, alertFilename(index), data, false, nil, nil))
	require.NoError(t, closeFile(dir))
}

func TestAlertLog_Rotation_KeepsThree(t *testing.T) {
	s := alertFixture(t)
	host, err := s.HostID()
	require.NoError(t, err)
	first, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	active := fullAlertSegment(t, host, &first)
	seedAlertSegment(t, s, 0, active)
	older := first
	older.ID = "11111111111111111111111111111111"
	older.Agent = "older"
	oldest := older
	oldest.ID = "22222222222222222222222222222222"
	oldest.Agent = "oldest"
	one, err := encodeAlertSegment([]Alert{older})
	require.NoError(t, err)
	seedAlertSegment(t, s, 1, one)
	two, err := encodeAlertSegment([]Alert{oldest})
	require.NoError(t, err)
	seedAlertSegment(t, s, 2, two)
	in := alertInput()
	in.Agent = "new-agent"
	_, err = s.RecordAlert(t.Context(), in, alertTime())
	require.NoError(t, err)
	dir, err := s.directory(false)
	require.NoError(t, err)
	defer func() { require.NoError(t, closeFile(dir)) }()
	seg, err := readAlertSegments(dir, host)
	require.NoError(t, err)
	require.Len(t, seg[0].rows, 1)
	require.Equal(t, "new-agent", seg[0].rows[0].Agent)
	require.Equal(t, active, seg[1].data)
	require.Equal(t, one, seg[2].data)
	for i := range seg {
		require.LessOrEqual(t, len(seg[i].data), maxRecordSize)
	}
	_, err = readData(dir, "alerts.3.jsonl")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestAlertRaise_EvictedWindow_PreservesCount(t *testing.T) {
	s := alertFixture(t)
	first, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	for i := range 5 {
		seedAlertSegment(t, s, 0, fullAlertSegment(t, first.HostID, nil))
		in := alertInput()
		in.Agent = fmt.Sprintf("rotation-%d", i)
		_, err = s.RecordAlert(t.Context(), in, alertTime().Add(time.Minute))
		require.NoError(t, err)
	}
	restarted := New(s.env)
	next, err := restarted.RecordAlert(t.Context(), alertInput(), alertTime().Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, first.ID, next.ID)
	require.Equal(t, uint64(2), next.Count)
}

func TestAlertLog_CountGrowthAtLimit_ReintroducesLogicalRow(t *testing.T) {
	for _, index := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			s := alertFixture(t)
			var row Alert
			var err error
			for range 9 {
				row, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
				require.NoError(t, err)
			}
			if index > 0 {
				seedAlertSegment(t, s, 0, []byte{})
			}
			seedAlertSegment(t, s, index, fullAlertSegment(t, row.HostID, &row))
			row, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
			require.NoError(t, err)
			require.Equal(t, uint64(10), row.Count)
			rows, err := s.RecentAlerts(t.Context(), 0)
			require.NoError(t, err)
			count := 0
			for _, got := range rows {
				if got.ID == row.ID {
					count++
					require.Equal(t, uint64(10), got.Count)
				}
			}
			require.Equal(t, 1, count)
		})
	}
}

func TestAlertRecent_ConflictingCopies_Refuses(t *testing.T) {
	s := alertFixture(t)
	row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	row.Detail = "different"
	data, err := json.Marshal(row)
	require.NoError(t, err)
	seedAlertSegment(t, s, 1, append(data, '\n'))
	_, err = s.RecentAlerts(t.Context(), 0)
	require.Error(t, err)
}
