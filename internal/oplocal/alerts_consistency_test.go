// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests fail-closed consistency before any accepted projection or replacement.
package oplocal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func alertStateSnapshot(t *testing.T, s *State) map[string]string {
	t.Helper()
	path, err := s.Path()
	require.NoError(t, err)
	entries, err := os.ReadDir(path)
	require.NoError(t, err)
	result := map[string]string{}
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join(path, entry.Name()))
		require.NoError(t, readErr)
		result[entry.Name()] = string(data)
	}
	return result
}

func TestAlertRecovery_InconsistentRate_RefusesWithoutProjection(t *testing.T) {
	for _, kind := range []string{"malformed", "foreign", "key", "greater-count"} {
		t.Run(kind, func(t *testing.T) {
			s := alertFixture(t)
			row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
			require.NoError(t, err)
			injected := errors.New("stop after preparation")
			s.alertStage = func(stage string) error {
				if stage == "after:alerts-pending.json" {
					return injected
				}
				return nil
			}
			_, err = s.RecordAlert(t.Context(), alertInput(), alertTime().Add(time.Minute))
			require.ErrorIs(t, err, injected)
			s.alertStage = nil
			dir, err := s.directory(false)
			require.NoError(t, err)
			name := alertRateName(row.Agent, row.Class)
			switch kind {
			case "malformed":
				require.NoError(t, writeData(dir, name, []byte("{"), false, nil, nil))
			case "foreign":
				data, readErr := readData(dir, name)
				require.NoError(t, readErr)
				data = bytes.Replace(data, []byte(row.HostID), []byte("00000000000000000000000000000000"), 1)
				require.NoError(t, writeData(dir, name, data, false, nil, nil))
			case "key":
				row.Agent = "different-agent"
				require.NoError(t, s.writeAlertRecord(dir, name, row.HostID, row))
			case "greater-count":
				row.Count = 3
				row.LastAt = alertTime().Add(2 * time.Minute)
				require.NoError(t, s.writeAlertRecord(dir, name, row.HostID, row))
			}
			require.NoError(t, closeFile(dir))
			before := alertStateSnapshot(t, s)
			_, err = New(s.env).RecordAlert(t.Context(), alertInput(), alertTime().Add(3*time.Minute))
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}

func TestAlertRaise_ImmutableRateMismatch_RefusesBeforePrepare(t *testing.T) {
	s := alertFixture(t)
	row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	row.FirstAt = row.FirstAt.Add(-time.Minute)
	dir, err := s.directory(false)
	require.NoError(t, err)
	require.NoError(t, s.writeAlertRecord(dir, alertRateName(row.Agent, row.Class), row.HostID, row))
	require.NoError(t, closeFile(dir))
	before := alertStateSnapshot(t, s)
	_, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestAlertRaise_RateBehindRetainedCount_RefusesBeforePrepare(t *testing.T) {
	s := alertFixture(t)
	first, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	_, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	dir, err := s.directory(false)
	require.NoError(t, err)
	require.NoError(t, s.writeAlertRecord(dir, alertRateName(first.Agent, first.Class), first.HostID, first))
	require.NoError(t, closeFile(dir))
	before := alertStateSnapshot(t, s)
	_, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestAlertRaise_MissingUnexpiredRate_RefusesBeforePrepare(t *testing.T) {
	s := alertFixture(t)
	row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	path, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, os.Rename(filepath.Join(path, alertRateName(row.Agent, row.Class)), filepath.Join(path, "saved-rate.json")))
	before := alertStateSnapshot(t, s)
	_, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestAlertDecode_OutsideRollingWindow_Refuses(t *testing.T) {
	row := alertInput()
	row.ID = "0123456789abcdef0123456789abcdef"
	row.HostID = "11111111111111111111111111111111"
	row.FirstAt = alertTime()
	row.LastAt = alertTime().Add(time.Hour)
	row.Count = 1
	data, err := encodeAlertSegment([]Alert{row})
	require.NoError(t, err)
	_, err = decodeAlertSegment(data, row.HostID)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func stoppedAlertPending(t *testing.T, kind string) (*State, alertPending) {
	t.Helper()
	s := alertFixture(t)
	first, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	input := alertInput()
	switch kind {
	case "append":
		input.Agent = "pending-agent"
	case "rotate":
		seedAlertSegment(t, s, 0, fullAlertSegment(t, first.HostID, nil))
		input.Agent = "pending-agent"
	case "update-archive":
		data, encodeErr := encodeAlertSegment([]Alert{first})
		require.NoError(t, encodeErr)
		seedAlertSegment(t, s, 1, data)
		seedAlertSegment(t, s, 0, []byte{})
	}
	injected := errors.New("stop accepted pending")
	s.alertStage = func(stage string) error {
		if stage == "after:alerts-pending.json" {
			return injected
		}
		return nil
	}
	_, err = s.RecordAlert(t.Context(), input, alertTime().Add(time.Minute))
	require.ErrorIs(t, err, injected)
	s.alertStage = nil
	dir, err := s.directory(false)
	require.NoError(t, err)
	var pending alertPending
	present, err := readAlertRecord(dir, "alerts-pending.json", first.HostID, &pending)
	require.NoError(t, err)
	require.True(t, present)
	require.NotNil(t, pending.Row)
	require.NoError(t, validateAlertPending(pending, first.HostID))
	require.NoError(t, closeFile(dir))
	return s, pending
}

func assertChangedPendingRefused(t *testing.T, s *State, pending alertPending, operation string) {
	t.Helper()
	dir, err := s.directory(false)
	require.NoError(t, err)
	require.NoError(t, s.writeAlertRecord(dir, "alerts-pending.json", pending.Row.HostID, pending))
	require.NoError(t, closeFile(dir))
	before := alertStateSnapshot(t, s)
	if operation == "Recent" {
		_, err = New(s.env).RecentAlerts(t.Context(), 0)
	} else {
		_, err = New(s.env).RecordAlert(t.Context(), alertInput(), alertTime().Add(2*time.Minute))
	}
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestAlertRecovery_PendingCountJump_RefusesUnchanged(t *testing.T) {
	for _, operation := range []string{"Recent", "Raise"} {
		t.Run(operation, func(t *testing.T) {
			s, pending := stoppedAlertPending(t, "update")
			require.Equal(t, uint64(2), pending.Row.Count)
			pending.Row.Count = 100
			assertChangedPendingRefused(t, s, pending, operation)
		})
	}
}

func TestAlertRecovery_ImpossiblePendingShape_RefusesUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		change func(*alertPending)
	}{
		{"append-target1", "append", func(p *alertPending) { p.Target = 1 }},
		{"append-target2", "append", func(p *alertPending) { p.Target = 2 }},
		{"append-active-invalid", "append", func(p *alertPending) { p.ActiveHash = "invalid" }},
		{"append-archive-invalid", "append", func(p *alertPending) { p.ArchiveHash = "invalid" }},
		{"append-target-conflict", "append", func(p *alertPending) { p.TargetHash = "absent" }},
		{"update-active-invalid", "update", func(p *alertPending) { p.ActiveHash = "invalid" }},
		{"update-archive-invalid", "update", func(p *alertPending) { p.ArchiveHash = "invalid" }},
		{"update-active-conflict", "update", func(p *alertPending) { p.TargetHash = "absent" }},
		{"update-archive-conflict", "update-archive", func(p *alertPending) { p.TargetHash = "absent" }},
		{"rotate-target-nonempty", "rotate", func(p *alertPending) { p.TargetHash = p.ActiveHash }},
	}
	for _, tc := range cases {
		for _, operation := range []string{"Recent", "Raise"} {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				s, pending := stoppedAlertPending(t, tc.kind)
				tc.change(&pending)
				assertChangedPendingRefused(t, s, pending, operation)
			})
		}
	}
}
