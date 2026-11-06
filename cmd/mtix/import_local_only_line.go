// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// localOnlyLine words the columns an import merge changed that no sync event
// carries (MTIX-112.17.16.16): each task with its columns, and what that
// means for the hub.
func localOnlyLine(changes []sqlite.LocalOnlyChange) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, fmt.Sprintf("%s (%s)", c.ID, strings.Join(c.Fields, ", ")))
	}
	return "Local only (no sync event carries these columns, so mtix sync push does not send them): " +
		strings.Join(parts, "; ")
}
