// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// hubAcceptedName is the record that an enrolment took the hub's version
// despite a diff report; doctor warns while it exists (MTIX-112.17.16.6).
const hubAcceptedName = "hub-accepted.json"

// HubAccepted is the durable record of an accept-hub enrolment.
type HubAccepted struct {
	Report     string `json:"report"`
	AsideDir   string `json:"aside_dir"`
	BackupPath string `json:"backup_path"`
	AcceptedAt string `json:"accepted_at"`
	// LostLocalReport lists the fields whose locally written value lost to the
	// hub at its head (MTIX-112.17.16.18); empty when none did.
	LostLocalReport string `json:"lost_local_report,omitempty"`
}

// HubAcceptedPath is where the record lives.
func HubAcceptedPath(mtixDir string) string {
	return filepath.Join(enrolDir(mtixDir), hubAcceptedName)
}

// writeHubAccepted records an accept-hub enrolment.
func writeHubAccepted(mtixDir string, a HubAccepted) error {
	body, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encode the accept-hub record: %w", err)
	}
	if err := os.MkdirAll(enrolDir(mtixDir), 0o700); err != nil {
		return fmt.Errorf("create the enrolment directory: %w", err)
	}
	if err := writeFileAtomically(HubAcceptedPath(mtixDir), body); err != nil {
		return fmt.Errorf("write the accept-hub record: %w", err)
	}
	return nil
}

// ReadHubAccepted returns the record, or nil when none exists.
func ReadHubAccepted(mtixDir string) (*HubAccepted, error) {
	body, err := os.ReadFile(HubAcceptedPath(mtixDir)) //nolint:gosec // G304: the worktree's own .mtix/enroll.d
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the accept-hub record: %w", err)
	}
	var a HubAccepted
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("decode the accept-hub record: %w", err)
	}
	return &a, nil
}

func removeHubAccepted(mtixDir string) error {
	if err := os.Remove(HubAcceptedPath(mtixDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the accept-hub record: %w", err)
	}
	return nil
}
