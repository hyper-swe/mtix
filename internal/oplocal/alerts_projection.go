// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"encoding/hex"
	"os"
	"strings"
)

func logicalAlertRows(segments [3]alertSegment, pending *Alert) ([]Alert, error) {
	byID := map[string]Alert{}
	for _, seg := range segments {
		for _, row := range seg.rows {
			if err := mergeAlertRow(byID, row); err != nil {
				return nil, err
			}
		}
	}
	if pending != nil {
		if err := mergeAlertRow(byID, *pending); err != nil {
			return nil, err
		}
	}
	rows := make([]Alert, 0, len(byID))
	for _, row := range byID {
		rows = append(rows, row)
	}
	return rows, nil
}
func mergeAlertRow(rows map[string]Alert, row Alert) error {
	if previous, ok := rows[row.ID]; ok {
		merged, err := reconcileAlert(previous, row)
		if err != nil {
			return err
		}
		row = merged
	}
	rows[row.ID] = row
	return nil
}
func prepareAlertProjection(row Alert, segments [3]alertSegment) (alertPending, error) {
	p := alertPending{Row: &row, Target: 0, ActiveHash: alertHash(segments[0].data), ArchiveHash: alertHash(segments[1].data)}
	for i, seg := range segments {
		for _, old := range seg.rows {
			if old.ID == row.ID {
				updated := replaceAlertRow(seg.rows, row)
				if _, err := encodeAlertSegment(updated); err == nil {
					p.Kind = "update"
					p.Target = i
					p.TargetHash = alertHash(seg.data)
					return p, nil
				}
			}
		}
	}
	active := append(append([]Alert{}, segments[0].rows...), row)
	if _, err := encodeAlertSegment(active); err == nil {
		p.Kind = "append"
		p.TargetHash = p.ActiveHash
		return p, nil
	}
	if _, err := encodeAlertSegment([]Alert{row}); err != nil {
		return p, err
	}
	p.Kind = "rotate"
	return p, nil
}
func replaceAlertRow(rows []Alert, row Alert) []Alert {
	result := append([]Alert{}, rows...)
	for i := range result {
		if result[i].ID == row.ID {
			result[i] = row
		}
	}
	return result
}
func projectionComplete(p alertPending, segments [3]alertSegment) bool {
	for _, row := range segments[p.Target].rows {
		if row.ID == p.Row.ID && row.Count == p.Row.Count && row.LastAt.Equal(p.Row.LastAt) && row.Detail == p.Row.Detail && row.Fix == p.Row.Fix {
			return true
		}
	}
	return false
}
func (s *State) projectAlert(dir *os.File, p alertPending, segments [3]alertSegment) error {
	if projectionComplete(p, segments) {
		return nil
	}
	if p.Kind == "rotate" {
		return s.rotateAlert(dir, p, segments)
	}
	if alertHash(segments[p.Target].data) != p.TargetHash {
		return invalid("alert projection preimage changed")
	}
	rows := replaceAlertRow(segments[p.Target].rows, *p.Row)
	if p.Kind == "append" {
		rows = append(rows, *p.Row)
	}
	data, err := encodeAlertSegment(rows)
	if err != nil {
		return err
	}
	return s.commitAlertData(dir, alertFilename(p.Target), data, alertFilename(p.Target))
}
func (s *State) rotateAlert(dir *os.File, p alertPending, segments [3]alertSegment) error {
	copied := alertHash(segments[1].data) == p.ActiveHash
	if !copied {
		if alertHash(segments[0].data) != p.ActiveHash || alertHash(segments[1].data) != p.ArchiveHash {
			return invalid("alert rotation preimage changed")
		}
		if segments[1].data != nil {
			if err := s.commitAlertData(dir, alertFilename(2), segments[1].data, alertFilename(2)); err != nil {
				return err
			}
		}
		if segments[0].data != nil {
			if err := s.commitAlertData(dir, alertFilename(1), segments[0].data, alertFilename(1)); err != nil {
				return err
			}
		}
	}
	data, err := encodeAlertSegment([]Alert{*p.Row})
	if err != nil {
		return err
	}
	return s.commitAlertData(dir, alertFilename(0), data, alertFilename(0))
}
func validateAlertPending(p alertPending, host string) error {
	if p.Row == nil {
		return validateEmptyAlertPending(p)
	}
	if err := validateStoredAlert(*p.Row, host); err != nil {
		return err
	}
	if err := validatePermissionAlertPending(p); err != nil {
		return err
	}
	if p.Target < 0 || p.Target > 2 {
		return invalid("alert projection target is invalid")
	}
	return validateAlertProjectionShape(p)
}

func validateAlertProjectionShape(p alertPending) error {
	if !validAlertHash(p.ActiveHash) || !validAlertHash(p.ArchiveHash) {
		return invalid("alert projection preimages are invalid")
	}
	switch p.Kind {
	case "append":
		if p.Target != 0 || p.TargetHash != p.ActiveHash {
			return invalid("alert append metadata is invalid")
		}
	case "update":
		return validateAlertUpdatePreimage(p)
	case "rotate":
		if p.Target != 0 || p.TargetHash != "" {
			return invalid("alert rotation metadata is invalid")
		}
	default:
		return invalid("alert projection kind is invalid")
	}
	return nil
}

func validateAlertUpdatePreimage(p alertPending) error {
	if !validAlertHash(p.TargetHash) {
		return invalid("alert projection hash is invalid")
	}
	if p.Target == 0 && p.TargetHash != p.ActiveHash {
		return invalid("alert active update preimage conflicts")
	}
	if p.Target == 1 && p.TargetHash != p.ArchiveHash {
		return invalid("alert archive update preimage conflicts")
	}
	return nil
}

func validateEmptyAlertPending(p alertPending) error {
	if p.Kind != "" || p.Target != 0 || p.ActiveHash != "" || p.ArchiveHash != "" || p.TargetHash != "" || p.PermissionOccurrence != nil {
		return invalid("empty alert transaction is invalid")
	}
	return nil
}
func validAlertHash(hash string) bool {
	if hash == "absent" {
		return true
	}
	data, err := hex.DecodeString(hash)
	return err == nil && len(data) == 32 && strings.ToLower(hash) == hash
}
