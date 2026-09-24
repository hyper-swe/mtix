// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// validateExportTimes checks every time value of an export before import
// writes anything (MTIX-95.31.1, from MTIX-95.22). A value must be RFC 3339
// and its UTC year must be 1..9999 (model.IsStorableTime): anything else is
// text no reader can parse back, so every later read of the node, and of
// its project, would fail. An empty optional value is allowed. The error
// names the record and the field and wraps model.ErrInvalidInput.
func validateExportTimes(data *ExportData) error {
	for i := range data.Nodes {
		if err := validateNodeTimes(&data.Nodes[i]); err != nil {
			return err
		}
	}
	for _, d := range data.Dependencies {
		if err := checkStorableTime(d.CreatedAt); err != nil {
			return fmt.Errorf("import rejected: dependency %s->%s created_at %w", d.FromID, d.ToID, err)
		}
	}
	for _, a := range data.Agents {
		if err := checkStorableTime(a.LastHeartbeat); err != nil {
			return fmt.Errorf("import rejected: agent %s last_heartbeat %w", a.AgentID, err)
		}
	}
	for _, sess := range data.Sessions {
		if err := checkStorableTime(sess.StartedAt); err != nil {
			return fmt.Errorf("import rejected: session %s started_at %w", sess.ID, err)
		}
		if err := checkStorableTime(sess.EndedAt); err != nil {
			return fmt.Errorf("import rejected: session %s ended_at %w", sess.ID, err)
		}
	}
	return nil
}

// validateNodeTimes checks the time columns of one exported node and the
// time of each of its annotations and activity entries (MTIX-95.31.1).
func validateNodeTimes(n *exportNode) error {
	columns := []struct{ name, value string }{
		{"created_at", n.CreatedAt}, {"updated_at", n.UpdatedAt},
		{"closed_at", n.ClosedAt}, {"defer_until", n.DeferUntil},
		{"deleted_at", n.DeletedAt}, {"invalidated_at", n.InvalidatedAt},
	}
	for _, c := range columns {
		if err := checkStorableTime(c.value); err != nil {
			return fmt.Errorf("import rejected: node %s %s %w", n.ID, c.name, err)
		}
	}
	for i := range n.Annotations {
		if at := n.Annotations[i].CreatedAt; !model.IsStorableTime(at) {
			return fmt.Errorf("import rejected: node %s annotations[%d] created_at %s: UTC year must be 1..9999: %w",
				n.ID, i, at.Format(time.RFC3339), model.ErrInvalidInput)
		}
	}
	for i := range n.Activity {
		if at := n.Activity[i].CreatedAt; !model.IsStorableTime(at) {
			return fmt.Errorf("import rejected: node %s activity[%d] created_at %s: UTC year must be 1..9999: %w",
				n.ID, i, at.Format(time.RFC3339), model.ErrInvalidInput)
		}
	}
	return nil
}

// checkStorableTime reports why a stored-time text cannot be read back, or
// nil when it can (or is empty): it must parse as RFC 3339 and its UTC year
// must be 1..9999 (model.IsStorableTime).
func checkStorableTime(value string) error {
	if value == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return fmt.Errorf("%q is not an RFC 3339 time: %w: %w", value, model.ErrInvalidInput, err)
	}
	if !model.IsStorableTime(t) {
		return fmt.Errorf("%q: UTC year must be 1..9999: %w", value, model.ErrInvalidInput)
	}
	return nil
}
