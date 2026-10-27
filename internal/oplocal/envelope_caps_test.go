// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Operator-local envelope caps preserve unrelated settings and reject invalid bounds.
package oplocal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inboxformat "github.com/hyper-swe/mtix/internal/format"
)

func TestEnvelopeCaps_AbsentOrConfigured_ReadWithoutWrites(t *testing.T) {
	s := identityState(t)
	caps, reports, err := s.EnvelopeCaps()
	require.NoError(t, err)
	assert.Empty(t, reports)
	assert.Equal(t, inboxformat.Caps{}, caps)
	settings := map[string]any{"max_events": 2, "max_body_bytes": 16, "max_total_bytes": 1024, "retained": "value"}
	require.NoError(t, s.WriteJSON("wake", settings))
	caps, reports, err = s.EnvelopeCaps()
	require.NoError(t, err)
	assert.Empty(t, reports)
	assert.Equal(t, inboxformat.Caps{MaxEvents: 2, MaxBodyBytes: 16, MaxTotalBytes: 1024}, caps)
	var after map[string]any
	_, err = s.ReadJSON("wake", &after)
	require.NoError(t, err)
	assert.Equal(t, "value", after["retained"])
	assert.Contains(t, s.EnvelopeCapsRemedy(), "wake.json")
	assert.Contains(t, s.EnvelopeCapsRemedy(), "preserving host_id")
}

func TestEnvelopeCaps_ForeignHostRecord_RefusedWithRemedy(t *testing.T) {
	s := identityState(t)
	id, err := s.HostID()
	require.NoError(t, err)
	other := strings.Repeat("0", 32)
	if other == id {
		other = strings.Repeat("1", 32)
	}
	path, err := s.Path()
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]any{"host_id": other, "data": map[string]int{"max_events": 1}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(path, "wake.json"), raw, 0600))
	caps, reports, err := s.EnvelopeCaps()
	require.Error(t, err)
	assert.Zero(t, caps)
	require.Len(t, reports, 1)
	assert.Contains(t, err.Error(), "another host")
	fix := s.EnvelopeCapsRemedy(reports...)
	assert.Contains(t, fix, "private backup")
	assert.Contains(t, fix, "do not adopt foreign settings")
	assert.Contains(t, fix, "host-id")
	assert.Contains(t, fix, "0600")
}

func TestEnvelopeCaps_InvalidSettings_ReturnsError(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"null", map[string]any{"max_events": nil}},
		{"fraction", map[string]any{"max_events": 1.5}},
		{"string", map[string]any{"max_events": "2"}},
		{"negative", map[string]any{"max_body_bytes": -1}},
		{"tiny framing", map[string]any{"max_total_bytes": 1}},
		{"invalid object", []string{"unexpected"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := identityState(t)
			require.NoError(t, s.WriteJSON("wake", tt.value))
			_, _, err := s.EnvelopeCaps()
			assert.Error(t, err)
		})
	}
	s := New(Env{GOOS: "linux", Home: "relative"})
	_, _, err := s.EnvelopeCaps()
	assert.Error(t, err)
	assert.Contains(t, s.EnvelopeCapsRemedy(), "Restore an operator-owned")
}

func TestEnvelopeCapsRemedy_CoversScopedAndLegacyOverrides(t *testing.T) {
	s := identityState(t)
	require.NoError(t, s.WriteJSON("wake", map[string]any{
		"host":   map[string]any{"wake.envelope_max_total_bytes": 1},
		"agents": map[string]any{"worker": map[string]any{"wake.envelope_max_body_bytes": -1}},
	}))
	_, _, err := s.EnvelopeCapsForAgent("worker")
	require.Error(t, err)
	fix := s.EnvelopeCapsRemedy()
	for _, required := range []string{"data.host", "data.agents", "wake.envelope_max_events", "wake.envelope_max_body_bytes", "wake.envelope_max_total_bytes", "max_events", "preserving host_id"} {
		assert.Contains(t, fix, required)
	}
}
