// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hyper-swe/mtix/internal/model"
)

func wakeBudgetName(key model.WakeRecipient) (string, error) {
	if !validLearnedID(key.AgentID) || !wakeBudgetIdentity(key.HostID) || !wakeBudgetIdentity(key.UID) {
		return "", invalid("wake budget recipient is invalid")
	}
	// Every component rejects NUL, so the tuple has an unambiguous encoding.
	sum := sha256.Sum256([]byte(key.AgentID + "\x00" + key.HostID + "\x00" + key.UID))
	return "wake-budget-" + hex.EncodeToString(sum[:24]), nil
}

func wakeBudgetIdentity(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validateWakeBudgetMirror(m WakeBudgetMirror) error {
	if m.Version != 1 || m.Unacked < 0 || m.Sources == nil || m.Credits == nil {
		return invalid("wake budget mirror metadata is invalid")
	}
	if _, err := wakeBudgetName(m.Recipient); err != nil {
		return err
	}
	for source, row := range m.Sources {
		if err := validateWakeBudgetSource(source, row); err != nil {
			return err
		}
	}
	if err := validateWakeBudgetCredits(m.Credits); err != nil {
		return err
	}
	return validateWakeBudgetTimes(m)
}

func validateWakeBudgetTimes(m WakeBudgetMirror) error {
	if (m.Unacked > 0 || !m.LastWakeAt.IsZero()) && !wakeBudgetTime(m.LastWakeAt) {
		return invalid("wake budget last attempt time is invalid")
	}
	if (!m.NextWakeAt.IsZero() && !wakeBudgetTime(m.NextWakeAt)) || (!m.LastRewindAt.IsZero() && !wakeBudgetTime(m.LastRewindAt)) || (!m.ObservedAt.IsZero() && !wakeBudgetTime(m.ObservedAt)) {
		return invalid("wake budget observation time is invalid")
	}
	return nil
}

func validateWakeBudgetSource(source string, row WakeBudgetSource) error {
	id, err := uuid.Parse(source)
	if err != nil || id == uuid.Nil || id.String() != source || row.Total < 0 || row.Committed < 0 || row.Committed > row.Total || row.AckMark < 0 {
		return invalid("wake budget source is invalid")
	}
	if row.Pending != "" && (!wakeBudgetIdentity(row.Pending) || row.Total <= row.Committed) {
		return invalid("wake budget pending acceptance is invalid")
	}
	if row.Total > row.Committed && row.Pending == "" {
		return invalid("wake budget counter has no pending acceptance")
	}
	return nil
}

func validateWakeBudgetCredits(credits []WakeBudgetCredit) error {
	var total int64
	for _, credit := range credits {
		if credit.Count <= 0 || !wakeBudgetTime(credit.At) || credit.Count > math.MaxInt64-total {
			return invalid("wake budget rolling credits are invalid")
		}
		total += credit.Count
	}
	return nil
}

func wakeBudgetTime(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999 && offset == 0
}
