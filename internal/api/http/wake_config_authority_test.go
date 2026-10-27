// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSetConfig_WakeAuthority_RefusedEvenWithServerTerminalPolicy(t *testing.T) {
	for _, tt := range []struct{ key, value string }{
		{"wake.max_hop_depth", "4096"}, {"wake.max_hop_depth", "4"}, {"wake.live", "on"}, {"wake.live", "off"}, {"inbox.strict_board_identity", "false"},
	} {
		t.Run(tt.key+"/"+tt.value, func(t *testing.T) {
			state := testutil.OperatorState(t)
			s := testServer(t)
			// A server-side TTY policy is deliberately permissive; it cannot authorize the client.
			cs, err := service.NewConfigService("")
			require.NoError(t, err)
			cs = cs.WithWakeSettings(service.WakeConfigOptions{State: state, RequireTTY: func() error { return nil }})
			s.configSvc = cs
			_, err = cs.Set("wake.max_hop_depth", "8")
			require.NoError(t, err)
			_, err = cs.Set("wake.live", "off")
			require.NoError(t, err)
			s.configWriter = cs
			dir, err := state.Path()
			require.NoError(t, err)
			before, err := os.ReadFile(filepath.Join(dir, "wake.json"))
			require.NoError(t, err)
			payload, err := json.Marshal(map[string]string{tt.key: tt.value, "prefix": "UNAUTHORIZED"})
			require.NoError(t, err)
			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, apiRequest(http.MethodPatch, "/api/v1/admin/config", string(payload)))
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "mtix config set")
			after, err := os.ReadFile(filepath.Join(dir, "wake.json"))
			require.NoError(t, err)
			require.Equal(t, before, after)
			prefix, err := s.configSvc.Get("prefix")
			require.NoError(t, err)
			require.Equal(t, "PROJ", prefix, "mixed request must be rejected before any write")
		})
	}
}
