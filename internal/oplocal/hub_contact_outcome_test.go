// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package oplocal

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHubContact_UntrustedLabels_NeverEscapeState(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/work/outcome/.mtix")
	secret := "postgres://user:sentinel-password@private.invalid/db"
	for _, ok := range []bool{false, true} {
		require.NoError(t, s.WriteJSON("hubcontact-"+key, map[string]any{
			"at": time.Now(), "ok": ok, "route": secret, "error_class": secret}))
		rec, present, err := s.ReadHubContact(key)
		require.NoError(t, err)
		require.True(t, present)
		raw, err := json.Marshal(rec)
		require.NoError(t, err)
		require.NotContains(t, string(raw), secret)
		require.Equal(t, "unknown", rec.Route)
		if !ok {
			require.Contains(t, string(raw), `"error_class":"pull_failed"`)
		}
		require.NoError(t, s.RecordHubContact(key, rec))
		var stored map[string]any
		_, err = s.ReadJSON("hubcontact-"+key, &stored)
		require.NoError(t, err)
		require.Equal(t, "unknown", stored["route"])
	}
}

func TestHubContact_LegacyAndKnownClasses_RoundTrip(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/work/outcome/.mtix")
	for _, class := range []string{"auth", "refused", "unreachable", "timeout", "pull_failed", ""} {
		require.NoError(t, s.WriteJSON("hubcontact-"+key, map[string]any{"at": time.Now(), "ok": class == "", "route": "poll", "error_class": class}))
		rec, present, err := s.ReadHubContact(key)
		require.NoError(t, err)
		require.True(t, present)
		raw, err := json.Marshal(rec)
		require.NoError(t, err)
		if class == "" {
			require.True(t, rec.OK)
			require.NotContains(t, string(raw), "error_class")
		} else {
			require.Contains(t, string(raw), class)
		}
	}
}
