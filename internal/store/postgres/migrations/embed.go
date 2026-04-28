// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package migrations embeds the Postgres hub schema SQL files per
// MTIX-15.2.5. The files are NOT executed by this package; the
// MTIX-15.3 transport reads them via Files() and Read() and runs them
// inside a single PG transaction guarded by an advisory lock.
package migrations

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.sql
var fs embed.FS

// Files returns the migration filenames in lexical (numeric-prefix) order.
// Caller MUST execute them in this order; the numbering encodes the
// dependency chain (002 references sync_events from 001 via FK, etc.).
func Files() ([]string, error) {
	entries, err := fs.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Read returns the file contents for the given migration filename.
// Returns an error if the file does not exist.
func Read(name string) (string, error) {
	b, err := fs.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("read migration %s: %w", name, err)
	}
	return string(b), nil
}
