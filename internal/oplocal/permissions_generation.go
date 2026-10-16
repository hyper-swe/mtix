// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"crypto/rand"
	"encoding/hex"
)

func newPermissionGeneration() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", failure("permission generation unavailable", err)
	}
	return hex.EncodeToString(value), nil
}

// Legacy quiescent markers decode without mutation. An accepted new transaction
// supersedes them; accepted legacy pending work upgrades before projection.
func (s *State) ensurePermissionGeneration(tx permissionTransaction, p permissionPending) (permissionPending, error) {
	if p.Generation != "" || p.Batch == nil {
		return p, nil
	}
	if err := guardPermissionBoard(tx, p); err != nil {
		return p, err
	}
	if err := validateLegacyGenerationUpgrade(tx, p); err != nil {
		return p, err
	}
	generation, err := newPermissionGeneration()
	if err != nil {
		return p, err
	}
	p.Generation = generation
	if operationErr := s.commitPermissionRecord(tx.dir, "permissions-pending.json", tx.host, p); operationErr != nil {
		return p, operationErr
	}
	return p, nil
}
func validateLegacyGenerationUpgrade(tx permissionTransaction, p permissionPending) error {
	if err := tx.ctx.Err(); err != nil {
		return err
	}
	if err := validatePermissionPending(p); err != nil {
		return err
	}
	if p.Batch.NodeID != "" && tx.publish == nil {
		return invalid("permission recovery publisher is required")
	}
	if _, err := validatePermissionReplayPending(tx.dir, tx.host, p); err != nil {
		return err
	}
	segments, err := readConfirmationSegments(tx.dir, tx.host)
	if err != nil {
		return err
	}
	if _, err = logicalConfirmations(segments, p, tx.host); err != nil {
		return err
	}
	data, grants, err := readPermissionGrants(tx.dir, tx.host)
	if err != nil {
		return err
	}
	_, err = permissionGrantProjection(data, grants, *p.Batch, tx.host)
	return err
}
func clearedPermissionPending(p permissionPending) permissionPending {
	return permissionPending{Generation: p.Generation, CompletedID: p.Batch.ID}
}

func (s *State) preparePermissionFinish(tx permissionTransaction, p permissionPending) (permissionPending, error) {
	if err := guardPermissionBoard(tx, p); err != nil {
		return p, err
	}
	if err := tx.ctx.Err(); err != nil {
		return p, err
	}
	if p.Batch.NodeID != "" && tx.publish == nil {
		return p, invalid("permission recovery publisher is required")
	}
	return s.ensurePermissionGeneration(tx, p)
}
