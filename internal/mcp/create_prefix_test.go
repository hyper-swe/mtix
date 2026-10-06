// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Local create boundaries share FR-2.1a prefix validation (MTIX-107.3).
package mcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestCreateMCP_PrefixGrammar_ReturnsSharedError(t *testing.T) {
	for _, prefix := range []string{"TEST_BAD", "test", "1TEST", "ABCDEFGHIJKLMNOPQRSTU", "TEST%", "A", "TEST-DEV-OPS", "ABCDEFGHIJKLMNOPQRST", ""} {
		t.Run(prefix, func(t *testing.T) {
			st := newInboxTestStore(t)
			reg := NewToolRegistry()
			svc := service.NewNodeService(st, nil, nil, nil, func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) })
			RegisterNodeTools(reg, svc, st, WithPrimaryProject("TEST"))
			raw, err := json.Marshal(map[string]string{"title": "Prefix boundary", "project": prefix})
			require.NoError(t, err)
			result, err := reg.Call(t.Context(), "mtix_create", raw)
			if expected := model.ValidatePrefix(prefix); prefix != "" && expected != nil {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				assert.EqualError(t, err, expected.Error())
				assert.Nil(t, result)
				projects, err := st.DistinctProjects(t.Context())
				require.NoError(t, err)
				assert.Empty(t, projects)
				return
			}
			require.NoError(t, err)
			require.False(t, result.IsError)
			var n model.Node
			require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &n))
			if prefix == "" {
				prefix = "TEST"
			}
			assert.Equal(t, prefix, n.Project)
			assert.Equal(t, prefix+"-1", n.ID)
		})
	}
}

func TestCreateMCP_InvalidPrimaryPrefix_ReturnsSharedError(t *testing.T) {
	for _, raw := range []string{`{"title":"Missing project"}`, `{"title":"Empty project","project":""}`} {
		t.Run(raw, func(t *testing.T) {
			st := newInboxTestStore(t)
			reg := NewToolRegistry()
			svc := service.NewNodeService(st, nil, nil, nil, func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) })
			RegisterNodeTools(reg, svc, st, WithPrimaryProject("TEST_BAD"))
			result, err := reg.Call(t.Context(), "mtix_create", json.RawMessage(raw))
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.EqualError(t, err, model.ValidatePrefix("TEST_BAD").Error())
			assert.Nil(t, result)
		})
	}
}
