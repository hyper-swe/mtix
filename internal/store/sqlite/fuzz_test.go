// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store"
)

// fuzzStore creates a temporary SQLite store for fuzz testing.
func fuzzStore(f *testing.F) *Store {
	f.Helper()

	tmpDir := f.TempDir()
	dbDir := filepath.Join(tmpDir, "fuzz")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		f.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	st, err := New(dbDir, logger)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = st.Close() })

	return st
}

// FuzzSearchQuery tests that SearchNodes does not panic on arbitrary input.
// Property: if search succeeds, results should have non-empty IDs.
func FuzzSearchQuery(f *testing.F) {
	// Seed corpus.
	f.Add("hello")
	f.Add("")
	f.Add("SELECT * FROM nodes")
	f.Add("'; DROP TABLE nodes; --")
	f.Add("test OR 1=1")
	f.Add("authentication AND login")
	f.Add("*")
	f.Add("\"exact phrase\"")
	f.Add("node:title")
	f.Add("(")
	f.Add(")")
	f.Add("a b c d e f g h i j k l m n o p q r s t u v w x y z")

	st := fuzzStore(f)
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, query string) {
		// SearchNodes should never panic regardless of input.
		// It may return an error for empty queries or invalid FTS syntax,
		// but should never crash.
		results, total, err := st.SearchNodes(ctx, query,
			store.NodeFilter{}, store.ListOptions{Limit: 10})

		if err == nil {
			// If search succeeds, total should be non-negative.
			if total < 0 {
				t.Errorf("SearchNodes(%q) returned negative total: %d", query, total)
			}
			// All results should have non-empty IDs.
			for i, r := range results {
				if r.ID == "" {
					t.Errorf("SearchNodes(%q) result[%d] has empty ID", query, i)
				}
			}
		}
	})
}

// FuzzJSONImport tests that Import does not panic on arbitrary JSON input.
// Property: if Import succeeds, the result should have non-negative counts.
func FuzzJSONImport(f *testing.F) {
	// Seed corpus: valid and malformed export JSON.
	validExport := `{"version":1,"exported_at":"2026-03-10T12:00:00Z","mtix_version":"0.1.0","project":"FUZZ","nodes":[],"dependencies":[],"agents":[],"sessions":[],"node_count":0,"checksum":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`
	f.Add(validExport)
	f.Add(`{}`)
	f.Add(`{"version":1}`)
	f.Add(`{"nodes":null}`)
	f.Add(`null`)
	f.Add(`{"version":1,"node_count":5,"nodes":[],"checksum":"abc"}`)
	f.Add(`not json at all`)
	f.Add(`{"version":1,"nodes":[{"id":"A","title":"test"}],"node_count":1}`)

	st := fuzzStore(f)
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, data string) {
		var export ExportData
		if err := json.Unmarshal([]byte(data), &export); err != nil {
			return // Not valid JSON — skip.
		}

		// Import should never panic regardless of input.
		// It should return an error for invalid data.
		result, err := st.Import(ctx, &export, ImportModeReplace, false)

		if err == nil && result != nil {
			// If import succeeds, counts should be non-negative.
			if result.NodesCreated < 0 {
				t.Errorf("Import returned negative NodesCreated: %d", result.NodesCreated)
			}
			if result.NodesUpdated < 0 {
				t.Errorf("Import returned negative NodesUpdated: %d", result.NodesUpdated)
			}
			if result.NodesSkipped < 0 {
				t.Errorf("Import returned negative NodesSkipped: %d", result.NodesSkipped)
			}
		}
	})
}
