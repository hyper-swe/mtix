// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// HoldsNodes reports whether the store holds any task row (MTIX-112.17.16.16).
// The replace import guard lets a replace into an empty store run, because
// it deletes nothing there.
func (s *Store) HoldsNodes(ctx context.Context) (bool, error) {
	var present bool
	// Does any task row exist? EXISTS stops at the first row, and the query
	// takes no input.
	if err := s.readDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM nodes)`).Scan(&present); err != nil {
		return false, fmt.Errorf("check whether the store holds tasks: %w", err)
	}
	return present, nil
}
