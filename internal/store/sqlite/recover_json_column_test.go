// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.1 in mtix recover (FR-26.5): the export projection
// now decodes the annotations and activity columns, and a row whose JSON
// column does not parse must still be salvaged, with that column dropped
// and a note naming it, never listed as lost.
package sqlite

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecover_UnreadableJSONColumn_SalvagesNodeWithNote verifies that a node
// with an unreadable annotations column (JSON of the wrong shape, which
// encoding/json would half-decode) and valid activity is recovered, keeps
// its readable activity, drops the unreadable column entirely, and is
// reported in a note naming it (MTIX-95.31.1).
func TestRecover_UnreadableJSONColumn_SalvagesNodeWithNote(t *testing.T) {
	s, dbPath := seedRecoverFixture(t)
	_, err := s.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET annotations = ?, activity = ? WHERE id = ?`,
		`[{"id": 5, "text": "half-decoded"}]`,
		`[{"id":"act-1","type":"comment","author":"a","text":"kept","created_at":"2026-06-01T00:00:00Z"}]`,
		"TEST-1")
	require.NoError(t, err)
	require.NoError(t, s.Close())

	res, err := Recover(context.Background(), dbPath, "", "test-version", slog.Default())
	require.NoError(t, err)

	assert.Contains(t, res.RecoveredIDs, "TEST-1")
	assert.NotContains(t, res.LostIDs, "TEST-1")
	var note string
	for _, n := range res.Notes {
		if strings.Contains(n, "TEST-1") && strings.Contains(n, "annotations") {
			note = n
		}
	}
	assert.NotEmpty(t, note, "a note must name the node and the dropped column; notes: %v", res.Notes)
	for _, n := range res.Export.Nodes {
		if n.ID == "TEST-1" {
			raw, marshalErr := jsonField(n, "activity")
			require.NoError(t, marshalErr)
			assert.Contains(t, raw, "kept", "the readable activity column is salvaged")
			anns, marshalErr := jsonField(n, "annotations")
			require.NoError(t, marshalErr)
			assert.Empty(t, anns, "an unreadable column is dropped, never half-decoded")
		}
	}
	importRoundTrip(t, res.Export)
}

// jsonField returns the raw JSON of key in the exported form of n, or ""
// when the export omits the key.
func jsonField(n exportNode, key string) (string, error) {
	raw, err := json.Marshal(n)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	return string(fields[key]), nil
}
