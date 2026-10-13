// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests independent validation of log rows, rate keys and prepared metadata.
package oplocal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestAlertRecent_InvalidStoredRows_Refuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]byte) []byte
	}{
		{"truncated", func(data []byte) []byte { return data[:len(data)-1] }},
		{"foreign", func(data []byte) []byte {
			var row Alert
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(data), &row))
			row.HostID = "00000000000000000000000000000000"
			result, err := json.Marshal(row)
			require.NoError(t, err)
			return append(result, '\n')
		}},
		{"duplicate-key", func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"agent":"test-agent"`), []byte(`"agent":"ignored","agent":"test-agent"`), 1)
		}},
		{"invalid-utf8", func(data []byte) []byte { return bytes.Replace(data, []byte("guard reported"), []byte{0xff}, 1) }},
		{"unknown-key", func(data []byte) []byte { return bytes.Replace(data, []byte(`"id":`), []byte(`"extra":true,"id":`), 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := alertFixture(t)
			row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
			require.NoError(t, err)
			data, err := encodeAlertSegment([]Alert{row})
			require.NoError(t, err)
			seedAlertSegment(t, s, 0, tc.change(data))
			_, err = s.RecentAlerts(t.Context(), 0)
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		})
	}
}

func TestAlertRaise_RateMetadata_RejectsOverflowAndConflictingKey(t *testing.T) {
	for _, kind := range []string{"overflow", "key"} {
		t.Run(kind, func(t *testing.T) {
			s := alertFixture(t)
			row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
			require.NoError(t, err)
			name := alertRateName(row.Agent, row.Class)
			if kind == "overflow" {
				row.Count = math.MaxUint64
			} else {
				row.Agent = "other-agent"
			}
			dir, err := s.directory(false)
			require.NoError(t, err)
			require.NoError(t, s.writeAlertRecord(dir, name, row.HostID, row))
			require.NoError(t, closeFile(dir))
			_, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
			require.Error(t, err)
			dir, err = s.directory(false)
			require.NoError(t, err)
			var pending alertPending
			_, err = readAlertRecord(dir, "alerts-pending.json", row.HostID, &pending)
			require.NoError(t, err)
			require.Nil(t, pending.Row)
			require.NoError(t, closeFile(dir))
		})
	}
}

func TestAlertRecent_InvalidPreparedHash_Refuses(t *testing.T) {
	s := alertFixture(t)
	row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	p := alertPending{Row: &row, Kind: "update", Target: 0, TargetHash: strings.Repeat("z", 64)}
	dir, err := s.directory(false)
	require.NoError(t, err)
	require.NoError(t, s.writeAlertRecord(dir, "alerts-pending.json", row.HostID, p))
	require.NoError(t, closeFile(dir))
	_, err = s.RecentAlerts(t.Context(), 0)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestAlertRaise_DiagnosticLimit_RejectsBeforeMutation(t *testing.T) {
	for _, size := range []int{4096, 4097} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := alertFixture(t)
			in := alertInput()
			in.Detail = strings.Repeat("x", size)
			in.Fix = ""
			_, err := s.RecordAlert(t.Context(), in, alertTime())
			if size == 4096 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				dir, pathErr := s.Path()
				require.NoError(t, pathErr)
				_, err = os.Stat(dir)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestAlertDecode_InvalidUTF8_Refuses(t *testing.T) {
	row := alertInput()
	row.ID = "0123456789abcdef0123456789abcdef"
	row.HostID = "11111111111111111111111111111111"
	row.Count = 1
	row.FirstAt = alertTime()
	row.LastAt = alertTime()
	data, err := encodeAlertSegment([]Alert{row})
	require.NoError(t, err)
	data = bytes.Replace(data, []byte("guard reported"), []byte{0xff}, 1)
	_, err = decodeAlertSegment(data, row.HostID)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestAlertRecords_ForeignOrMalformed_Refuses(t *testing.T) {
	for _, name := range []string{"alerts-pending.json", "rate"} {
		for _, kind := range []string{"foreign", "malformed", "oversized"} {
			t.Run(name+"-"+kind, func(t *testing.T) {
				s := alertFixture(t)
				row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
				require.NoError(t, err)
				target := name
				if name == "rate" {
					target = alertRateName(row.Agent, row.Class)
				}
				dir, err := s.directory(false)
				require.NoError(t, err)
				data, err := readData(dir, target)
				require.NoError(t, err)
				switch kind {
				case "foreign":
					data = bytes.Replace(data, []byte(row.HostID), []byte("00000000000000000000000000000000"), 1)
				case "malformed":
					data = []byte("{")
				case "oversized":
					data = bytes.Repeat([]byte(" "), maxRecordSize+1)
				}
				require.NoError(t, writeData(dir, target, data, false, nil, nil))
				require.NoError(t, closeFile(dir))
				_, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
				require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
				_, err = s.RecentAlerts(t.Context(), 0)
				require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
			})
		}
	}
}

func TestAlertState_MissingLock_RefusesWithoutCreatingFile(t *testing.T) {
	s := alertFixture(t)
	_, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	dir, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, os.Rename(filepath.Join(dir, "alerts.lock"), filepath.Join(dir, "saved-alerts.lock")))
	_, err = s.RecentAlerts(t.Context(), 0)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	_, err = os.Stat(filepath.Join(dir, "alerts.lock"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestAlertClasses_Registry_RejectsSupersededHold(t *testing.T) {
	classes := []string{AlertClassWakeHopDepth, AlertClassWakeBackoff, "wake.binary", "wake.contract-probe", "wake.probe-no-cold", "wake.cold-exit", "wake.nothing-acked", "wake.budget", "hooks.exec-untrusted", "hooks.webhook-untrusted", "wake.takeover", "wake.counter-rewind", "hooks.pin-placement", "wake.unconfirmed", "wake.version-skew"}
	for _, class := range classes {
		t.Run(class, func(t *testing.T) {
			row := alertInput()
			row.Class = class
			require.NoError(t, ValidateAlertInput(row))
		})
	}
	row := alertInput()
	row.Class = "wake.held-noapproval"
	require.ErrorIs(t, ValidateAlertInput(row), model.ErrInvalidInput)
}

func TestAlertRaise_ZeroObservation_RefusesBeforeCreation(t *testing.T) {
	s := alertFixture(t)
	_, err := s.RecordAlert(t.Context(), alertInput(), time.Time{})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	dir, err := s.Path()
	require.NoError(t, err)
	_, err = os.Stat(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
}
