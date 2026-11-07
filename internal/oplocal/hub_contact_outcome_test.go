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

func TestHubContact_RecordHostileLabels_RawStoredPayloadIsSafe(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/work/outcome/raw/.mtix")
	secret := "postgres://user:raw-write-secret@private.invalid/db"
	for _, ok := range []bool{false, true} {
		require.NoError(t, s.RecordHubContact(key, HubContact{At: time.Now(), Route: secret, OK: ok, ErrorClass: secret}))
		var raw map[string]any
		reports, err := s.ReadJSON("hubcontact-"+key, &raw)
		require.NoError(t, err)
		require.Empty(t, reports)
		encoded, err := json.Marshal(raw)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "raw-write-secret", "test the write filter without the read filter")
		require.Equal(t, "unknown", raw["route"])
		if ok {
			require.NotContains(t, raw, "error_class")
		} else {
			require.Equal(t, "pull_failed", raw["error_class"])
		}
	}
}

func TestHubContact_RealResult_RefreshesMalformedPayload(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/work/contact-malformed/.mtix")
	require.NoError(t, s.WriteJSON("hubcontact-"+key, map[string]any{"at": "postgres://user:stale-record-secret@private.invalid/db", "ok": false, "route": "poll"}))
	fresh := HubContact{At: time.Date(2026, 11, 6, 1, 0, 0, 0, time.UTC), Route: "pull", OK: true}
	require.Error(t, s.RecordHubContact("invalid-key", fresh))
	require.NoError(t, s.RecordHubContact(key, fresh))
	got, present, err := s.ReadHubContact(key)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, fresh, got)
	var raw map[string]any
	_, err = s.ReadJSON("hubcontact-"+key, &raw)
	require.NoError(t, err)
	text, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NotContains(t, string(text), "stale-record-secret")
}
