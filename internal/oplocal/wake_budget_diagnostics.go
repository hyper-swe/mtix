// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"context"
	"encoding/hex"
	"strings"
)

// WakeBudgetMirrors lists protected accounting without creating files or locks.
func (s *State) WakeBudgetMirrors(ctx context.Context) (result []WakeBudgetMirror, err error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, failure("read wake budget mirrors", contextErr)
	}
	result = []WakeBudgetMirror{}
	dir, err := s.directory(false)
	if missingState(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer closeWith(dir, &err)
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, failure("list wake budget mirrors", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, failure("read wake budget mirrors", err)
		}
		name, ok := wakeBudgetRecordName(entry.Name())
		if !ok {
			continue
		}
		mirror, present, err := s.readWakeBudgetRecord(name)
		if err != nil {
			return nil, err
		}
		if present {
			result = append(result, mirror)
		}
	}
	return result, nil
}

func wakeBudgetRecordName(file string) (string, bool) {
	name, ok := strings.CutSuffix(file, ".json")
	if !ok || !strings.HasPrefix(name, "wake-budget-") {
		return "", false
	}
	suffix := strings.TrimPrefix(name, "wake-budget-")
	decoded, err := hex.DecodeString(suffix)
	return name, err == nil && len(decoded) == 24 && strings.ToLower(suffix) == suffix
}
