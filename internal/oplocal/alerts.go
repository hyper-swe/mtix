// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// Alert is a host-specific counted operator diagnostic (MTIX-112.17.11).
type Alert struct {
	ID      string    `json:"id"`
	HostID  string    `json:"host_id"`
	Agent   string    `json:"agent"`
	Class   string    `json:"class"`
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
	Count   uint64    `json:"count"`
	Detail  string    `json:"detail"`
	Fix     string    `json:"fix"`
}

// RecordAlert persists one occurrence using the supplied observation time.
func (s *State) RecordAlert(ctx context.Context, input Alert, now time.Time) (Alert, error) {
	if inputErr := ValidateAlertInput(input); inputErr != nil {
		return Alert{}, inputErr
	}
	if now.IsZero() {
		return Alert{}, fmt.Errorf("alert observation time is invalid: %w", model.ErrInvalidInput)
	}
	var row Alert
	operationErr := s.withAlerts(ctx, true, func(dir *os.File, host string) error {
		var recordErr error
		row, recordErr = s.recordAlertLocked(dir, host, input, now.UTC())
		return recordErr
	})
	return row, operationErr
}

func (s *State) recordAlertLocked(dir *os.File, host string, input Alert, now time.Time) (Alert, error) {
	if recoveryErr := s.recoverAlert(dir, host); recoveryErr != nil {
		return Alert{}, recoveryErr
	}
	segments, err := readAlertSegments(dir, host)
	if err != nil {
		return Alert{}, err
	}
	if _, logicalErr := logicalAlertRows(segments, nil); logicalErr != nil {
		return Alert{}, logicalErr
	}
	row, err := nextAlertRow(dir, host, input, now, segments)
	if err != nil {
		return row, err
	}
	if proposedErr := validateProposedAlert(row, segments); proposedErr != nil {
		return row, proposedErr
	}
	pending, err := prepareAlertProjection(row, segments)
	if err != nil {
		return row, err
	}
	if writeErr := s.writeAlertRecord(dir, "alerts-pending.json", host, pending); writeErr != nil {
		return row, writeErr
	}
	if finishErr := s.finishAlert(dir, host, pending, segments); finishErr != nil {
		return row, finishErr
	}
	return row, pruneAlertRates(dir, host, now)
}

// RecentAlerts returns newest-first logical diagnostics; zero selects all,
// a positive limit selects a prefix, and a negative limit is invalid.
// Reads use existing state only, including accepted pending occurrences.
func (s *State) RecentAlerts(ctx context.Context, limit int) ([]Alert, error) {
	if limit < 0 {
		return nil, fmt.Errorf("alert limit is invalid: %w", model.ErrInvalidInput)
	}
	rows := []Alert{}
	operationErr := s.withAlerts(ctx, false, func(dir *os.File, host string) error {
		var readErr error
		rows, readErr = readRecentAlertsLocked(dir, host)
		return readErr
	})
	if operationErr != nil {
		return nil, operationErr
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].LastAt.Equal(rows[j].LastAt) {
			return rows[i].LastAt.After(rows[j].LastAt)
		}
		return rows[i].ID < rows[j].ID
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func readRecentAlertsLocked(dir *os.File, host string) ([]Alert, error) {
	segments, err := readAlertSegments(dir, host)
	if err != nil {
		return nil, err
	}
	var pending alertPending
	if _, readErr := readAlertRecord(dir, "alerts-pending.json", host, &pending); readErr != nil {
		return nil, readErr
	}
	if pendingErr := validateAlertPending(pending, host); pendingErr != nil {
		return nil, pendingErr
	}
	if pending.Row != nil {
		if acceptedErr := validateAcceptedAlertRate(dir, host, *pending.Row); acceptedErr != nil {
			return nil, acceptedErr
		}
	}
	rows, err := logicalAlertRows(segments, pending.Row)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if rateErr := validateAlertRate(dir, host, row); rateErr != nil {
			return nil, rateErr
		}
	}
	return rows, nil
}

func nextAlertRow(dir *os.File, host string, input Alert, now time.Time, segments [3]alertSegment) (Alert, error) {
	previous, present, err := readCurrentAlertRate(dir, host, input, segments)
	if err != nil {
		return Alert{}, err
	}
	if present {
		if now.Before(previous.FirstAt.Add(time.Hour)) {
			if previous.Count == math.MaxUint64 {
				return Alert{}, fmt.Errorf("alert count is exhausted: %w", model.ErrInvalidInput)
			}
			previous.Count++
			if now.After(previous.LastAt) {
				previous.LastAt = now
			}
			previous.Detail = input.Detail
			previous.Fix = input.Fix
			return previous, nil
		}
	}
	if !present {
		if missingErr := validateMissingAlertRate(input, now, segments); missingErr != nil {
			return Alert{}, missingErr
		}
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return Alert{}, failure("alert identifier unavailable", err)
	}
	return Alert{ID: hex.EncodeToString(id), HostID: host, Agent: input.Agent, Class: input.Class, Count: 1, FirstAt: now, LastAt: now, Detail: input.Detail, Fix: input.Fix}, nil
}

func validateAlertRate(dir *os.File, host string, row Alert) error {
	var rate Alert
	present, err := readAlertRecord(dir, alertRateName(row.Agent, row.Class), host, &rate)
	if err != nil || !present {
		return err
	}
	if validationErr := validateStoredAlert(rate, host); validationErr != nil {
		return validationErr
	}
	if rate.Agent != row.Agent || rate.Class != row.Class {
		return invalid("alert rate key conflicts")
	}
	if rate.ID == row.ID {
		_, err = reconcileAlert(rate, row)
	}
	return err
}

func (s *State) recoverAlert(dir *os.File, host string) error {
	var pending alertPending
	if _, err := readAlertRecord(dir, "alerts-pending.json", host, &pending); err != nil {
		return err
	}
	if err := validateAlertPending(pending, host); err != nil {
		return err
	}
	if pending.Row == nil {
		return nil
	}
	segments, err := readAlertSegments(dir, host)
	if err != nil {
		return err
	}
	if _, err = logicalAlertRows(segments, pending.Row); err != nil {
		return err
	}
	return s.finishAlert(dir, host, pending, segments)
}

func (s *State) finishAlert(dir *os.File, host string, pending alertPending, segments [3]alertSegment) error {
	if err := validateAcceptedAlertRate(dir, host, *pending.Row); err != nil {
		return err
	}
	if err := validateProposedAlert(*pending.Row, segments); err != nil {
		return err
	}
	if err := s.projectAlert(dir, pending, segments); err != nil {
		return err
	}
	if err := s.writeAlertRecord(dir, alertRateName(pending.Row.Agent, pending.Row.Class), host, *pending.Row); err != nil {
		return err
	}
	return s.writeAlertRecord(dir, "alerts-pending.json", host, alertPending{})
}

func readCurrentAlertRate(dir *os.File, host string, input Alert, segments [3]alertSegment) (Alert, bool, error) {
	var previous Alert
	present, err := readAlertRecord(dir, alertRateName(input.Agent, input.Class), host, &previous)
	if err != nil || !present {
		return previous, present, err
	}
	if validationErr := validateStoredAlert(previous, host); validationErr != nil {
		return Alert{}, false, validationErr
	}
	if previous.Agent != input.Agent || previous.Class != input.Class {
		return Alert{}, false, invalid("alert rate key conflicts")
	}
	if validationErr := validateProposedAlert(previous, segments); validationErr != nil {
		return Alert{}, false, validationErr
	}
	return previous, true, nil
}
