// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests refusal of malformed protected records and interrupted projections.
package oplocal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func writeRefusalRate(t *testing.T, s *State, row Alert) {
	t.Helper()
	dir, err := s.directory(false)
	require.NoError(t, err)
	require.NoError(t, s.writeAlertRecord(dir, alertRateName(alertInput().Agent, alertInput().Class), row.HostID, row))
	require.NoError(t, closeFile(dir))
}

func assertAlertRefusalUnchanged(t *testing.T, s *State, recent bool) {
	t.Helper()
	before := alertStateSnapshot(t, s)
	_, err := New(s.env).RecordAlert(t.Context(), alertInput(), alertTime().Add(3*time.Minute))
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.Equal(t, before, alertStateSnapshot(t, s))
	if recent {
		_, err = New(s.env).RecentAlerts(t.Context(), 0)
		require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		require.Equal(t, before, alertStateSnapshot(t, s))
	}
}

func TestAlertRefusal_RetainedCopiesAndRate_LeavesBytesUnchanged(t *testing.T) {
	for _, kind := range []string{"copies", "chronology", "rate-window", "rate-key", "rate-data"} {
		t.Run(kind, func(t *testing.T) {
			s := alertFixture(t)
			row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
			require.NoError(t, err)
			switch kind {
			case "copies":
				row.Detail = "conflicting copy"
			case "chronology":
				first := row
				first.LastAt = first.LastAt.Add(time.Minute)
				data, encodeErr := encodeAlertSegment([]Alert{first})
				require.NoError(t, encodeErr)
				seedAlertSegment(t, s, 0, data)
				row.Count = 2
			case "rate-window":
				row.Count = 0
			case "rate-key":
				row.Agent = "different-key"
			case "rate-data":
				row.Class = "unknown-class"
			}
			if strings.HasPrefix(kind, "rate-") {
				writeRefusalRate(t, s, row)
			} else {
				data, encodeErr := encodeAlertSegment([]Alert{row})
				require.NoError(t, encodeErr)
				seedAlertSegment(t, s, 1, data)
			}
			assertAlertRefusalUnchanged(t, s, true)
		})
	}
}

func TestAlertRefusal_AcceptedPendingAndRate_LeavesBytesUnchanged(t *testing.T) {
	for _, kind := range []string{"missing-rate", "invalid-rate", "different-window", "immutable-rate", "malformed-segment", "conflicting-pending", "newer-retained"} {
		t.Run(kind, func(t *testing.T) {
			s, pending := stoppedAlertPending(t, "update")
			row := *pending.Row
			switch kind {
			case "missing-rate":
				dir, pathErr := s.Path()
				require.NoError(t, pathErr)
				require.NoError(t, os.Rename(filepath.Join(dir, alertRateName(row.Agent, row.Class)), filepath.Join(dir, "saved-rate.json")))
			case "invalid-rate", "different-window", "immutable-rate":
				row.Count = 1
				switch kind {
				case "invalid-rate":
					row.Count = 0
				case "different-window":
					row.ID = "ffffffffffffffffffffffffffffffff"
				case "immutable-rate":
					row.FirstAt = row.FirstAt.Add(-time.Minute)
				}
				writeRefusalRate(t, s, row)
			case "malformed-segment":
				seedAlertSegment(t, s, 1, []byte("{\n"))
			case "conflicting-pending", "newer-retained":
				if kind == "conflicting-pending" {
					row.Detail = "conflicting pending copy"
				} else {
					row.Count++
				}
				data, encodeErr := encodeAlertSegment([]Alert{row})
				require.NoError(t, encodeErr)
				seedAlertSegment(t, s, 0, data)
			}
			assertAlertRefusalUnchanged(t, s, kind != "newer-retained")
		})
	}
}

func TestAlertRefusal_PendingMetadata_LeavesBytesUnchanged(t *testing.T) {
	for _, kind := range []string{"row-data", "row-window", "target-negative", "target-large", "kind", "target-hash", "empty-metadata"} {
		t.Run(kind, func(t *testing.T) {
			s, pending := stoppedAlertPending(t, "update")
			switch kind {
			case "row-data":
				pending.Row.Detail = "unsafe\x1b"
			case "row-window":
				pending.Row.Count = 0
			case "target-negative":
				pending.Target = -1
			case "target-large":
				pending.Target = 3
			case "kind":
				pending.Kind = "unsupported"
			case "target-hash":
				pending.TargetHash = strings.Repeat("z", 64)
			case "empty-metadata":
				host := pending.Row.HostID
				pending.Row = nil
				dir, openErr := s.directory(false)
				require.NoError(t, openErr)
				require.NoError(t, s.writeAlertRecord(dir, "alerts-pending.json", host, pending))
				require.NoError(t, closeFile(dir))
				assertAlertRefusalUnchanged(t, s, true)
				return
			}
			assertChangedPendingRefused(t, s, pending, "Recent")
			assertAlertRefusalUnchanged(t, s, true)
		})
	}
}

func TestAlertRefusal_ProjectionPreimageChanged_LeavesBytesUnchanged(t *testing.T) {
	for _, kind := range []string{"append", "rotate"} {
		t.Run(kind, func(t *testing.T) {
			s, pending := stoppedAlertPending(t, kind)
			unrelated := *pending.Row
			unrelated.ID = "ffffffffffffffffffffffffffffffff"
			unrelated.Agent = "unrelated-agent"
			data, err := encodeAlertSegment([]Alert{unrelated})
			require.NoError(t, err)
			seedAlertSegment(t, s, 0, data)
			assertAlertRefusalUnchanged(t, s, false)
		})
	}
}

func TestAlertRefusal_JSONShape_LeavesBytesUnchanged(t *testing.T) {
	for _, raw := range []string{"", "[]", "[1,{}]", "[", "[[[[[[[[[0]]]]]]]]]", "{1:2}", `{"row":}`, `{"row":[]}`, `{} {}`} {
		t.Run(strings.ReplaceAll(raw, "/", "_"), func(t *testing.T) {
			s, pending := stoppedAlertPending(t, "update")
			dir, err := s.directory(false)
			require.NoError(t, err)
			if raw == `{} {}` {
				data, readErr := readData(dir, "alerts-pending.json")
				require.NoError(t, readErr)
				require.NoError(t, writeData(dir, "alerts-pending.json", append(data, []byte(" {}")...), false, nil, nil))
			} else {
				envelope, encodeErr := json.Marshal(record{HostID: pending.Row.HostID, Data: json.RawMessage(raw)})
				if encodeErr != nil {
					envelope = []byte(`{"host_id":"` + pending.Row.HostID + `","data":` + raw + `}`)
				}
				require.NoError(t, writeData(dir, "alerts-pending.json", envelope, false, nil, nil))
			}
			require.NoError(t, closeFile(dir))
			assertAlertRefusalUnchanged(t, s, true)
		})
	}
}

func TestAlertRefusal_CanceledContext_CreatesNothing(t *testing.T) {
	s := alertFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.RecordAlert(ctx, alertInput(), alertTime())
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.RecentAlerts(ctx, 0)
	require.ErrorIs(t, err, context.Canceled)
	path, err := s.Path()
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestAlertRefusal_ExistingDirectoryWithoutAlerts_DoesNotCreate(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.Ensure())
	before := alertStateSnapshot(t, s)
	rows, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestAlertRefusal_InvalidHost_LeavesBytesUnchanged(t *testing.T) {
	s, _ := stoppedAlertPending(t, "update")
	dir, err := s.directory(false)
	require.NoError(t, err)
	require.NoError(t, writeData(dir, "host-id", []byte("not-a-host\n"), false, nil, nil))
	require.NoError(t, closeFile(dir))
	assertAlertRefusalUnchanged(t, s, true)
}

func TestAlertRefusal_InvalidClockCalendar_LeavesBytesUnchanged(t *testing.T) {
	s := alertFixture(t)
	_, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	before := alertStateSnapshot(t, s)
	_, err = s.RecordAlert(t.Context(), alertInput(), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestAlertRefusal_NativeLockErrors_LeavesBytesUnchanged(t *testing.T) {
	s := alertFixture(t)
	_, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	before := alertStateSnapshot(t, s)
	dir, err := s.directory(false)
	require.NoError(t, err)
	lock, err := openAlertLock(dir, false)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, awaitAlertLock(ctx, lock), context.Canceled)
	require.NoError(t, closeFile(lock))
	require.ErrorIs(t, awaitAlertLock(t.Context(), lock), model.ErrOperatorStateUnreadable)
	require.NoError(t, closeFile(dir))
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestAlertRefusal_InvalidConfiguredPath_LeavesBytesUnchanged(t *testing.T) {
	s := alertFixture(t)
	_, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	before := alertStateSnapshot(t, s)
	original := s.env.Values
	s.env.Values = map[string]string{"XDG_CONFIG_HOME": "relative", "APPDATA": "relative"}
	_, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	_, err = s.RecentAlerts(t.Context(), 0)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	s.env.Values = original
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestAlertRefusal_ProtectedPendingEncoding_LeavesBytesUnchanged(t *testing.T) {
	for _, kind := range []string{"record-limit", "calendar-range"} {
		t.Run(kind, func(t *testing.T) {
			s, pending := stoppedAlertPending(t, "update")
			if kind == "record-limit" {
				pending.Row.Detail = strings.Repeat("x", maxRecordSize)
			} else {
				pending.Row.LastAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			before := alertStateSnapshot(t, s)
			dir, err := s.directory(false)
			require.NoError(t, err)
			// The storage primitive enforces its own envelope bound and encoding
			// refusal independently of the earlier public-input validation.
			require.ErrorIs(t, s.writeAlertRecord(dir, "alerts-pending.json", pending.Row.HostID, pending), model.ErrOperatorStateUnreadable)
			require.NoError(t, closeFile(dir))
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}

func TestAlertRefusal_InflatedPendingProjection_LeavesBytesUnchanged(t *testing.T) {
	s := alertFixture(t)
	first, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	rows, err := decodeAlertSegment(fullAlertSegment(t, first.HostID, nil), first.HostID)
	require.NoError(t, err)
	rows[0].Detail = rows[0].Detail[:len(rows[0].Detail)-600]
	active, err := encodeAlertSegment(rows)
	require.NoError(t, err)
	seedAlertSegment(t, s, 0, active)
	s.alertStage = func(stage string) error {
		if stage == "after:alerts-pending.json" {
			return model.ErrOperatorStateUnreadable
		}
		return nil
	}
	input := alertInput()
	input.Agent = "pending-agent"
	_, err = s.RecordAlert(t.Context(), input, alertTime())
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	s.alertStage = nil
	dir, err := s.directory(false)
	require.NoError(t, err)
	var pending alertPending
	present, err := readAlertRecord(dir, "alerts-pending.json", first.HostID, &pending)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "append", pending.Kind)
	pending.Row.Detail = strings.Repeat("x", 4096-len(pending.Row.Fix))
	require.NoError(t, ValidateAlertInput(*pending.Row))
	require.NoError(t, s.writeAlertRecord(dir, "alerts-pending.json", first.HostID, pending))
	require.NoError(t, closeFile(dir))
	assertAlertRefusalUnchanged(t, s, false)
}
