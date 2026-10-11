// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func legacyOutboundReceipt(t *testing.T, s *SyncService, dir string, null bool) []byte {
	t.Helper()
	r := exchangePublishReference(t, s, dir)
	r.Source, r.BoardFileHash = "hub-pull", ""
	r.Boundary = &sqlite.ExchangeBoundary{LastSweepAt: "2026-10-01T00:00:00Z"}
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	var boundary map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fields["boundary"], &boundary))
	delete(boundary, "pending_outbound")
	if null {
		boundary["pending_outbound"] = json.RawMessage("null")
	}
	fields["boundary"], err = json.Marshal(boundary)
	require.NoError(t, err)
	raw, err = json.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(exchangePath(dir), raw, 0o600))
	return raw
}

func TestExchangeRecordStatus_LegacyOutboundBoundary_ReadOnlyUnknown(t *testing.T) {
	for _, null := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "null"}[null], func(t *testing.T) {
			s, dir := exchangeFixture(t)
			before := legacyOutboundReceipt(t, s, dir, null)
			require.NoError(t, s.saveExchangeReason(t.Context(), ""))
			var metaBefore string
			require.NoError(t, s.store.QueryRow(t.Context(), `SELECT json_group_object(key,value) FROM (SELECT key,value FROM meta ORDER BY key)`).Scan(&metaBefore))
			got, err := s.LastExchanged(t.Context(), dir)
			require.NoError(t, err, "historical receipt remains readable")
			require.Equal(t, "hub-pull", got.Source)
			require.Equal(t, "c9f695ea6ec28ab5cc4afc2b90326a66b0f4d9d26fad99f8cc0b50734f3e72bb", got.BoardHash)
			status, err := s.ExchangeRecordStatus(t.Context(), dir)
			require.NoError(t, err)
			require.True(t, status.Present)
			require.Equal(t, "outbound-boundary-unknown", status.Reason)
			after, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.Equal(t, before, after)
			var reason string
			require.NoError(t, s.store.QueryRow(t.Context(), `SELECT value FROM meta WHERE key=?`, exchangeReasonKey).Scan(&reason))
			require.Empty(t, reason, "inference must not persist a diagnostic")
			var metaAfter string
			require.NoError(t, s.store.QueryRow(t.Context(), `SELECT json_group_object(key,value) FROM (SELECT key,value FROM meta ORDER BY key)`).Scan(&metaAfter))
			require.Equal(t, metaBefore, metaAfter)
		})
	}
}

func TestExchangeRecordStatus_LegacyWithLaterOmission_PreservesSavedReason(t *testing.T) {
	s, dir := exchangeFixture(t)
	before := legacyOutboundReceipt(t, s, dir, false)
	require.NoError(t, s.saveExchangeReason(t.Context(), "partial-pull"))
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, "partial-pull", status.Reason)
	after, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func outboundReceiptValue(t *testing.T, s *SyncService, dir string, value string) []byte {
	t.Helper()
	before := legacyOutboundReceipt(t, s, dir, false)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(before, &fields))
	var boundary map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fields["boundary"], &boundary))
	boundary["pending_outbound"] = json.RawMessage(value)
	var err error
	fields["boundary"], err = json.Marshal(boundary)
	require.NoError(t, err)
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(exchangePath(dir), raw, 0o600))
	return raw
}

func TestLastExchanged_InvalidOutboundValue_RefusesWithoutRewrite(t *testing.T) {
	for _, value := range []string{"1", "-1", `"0"`, "0.5", "9223372036854775808", "true", "[]"} {
		t.Run(value, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			before := outboundReceiptValue(t, s, dir, value)
			_, err := s.LastExchanged(t.Context(), dir)
			require.ErrorIs(t, err, ErrNoExchangeRecord)
			after, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestExchangeRecordStatus_KnownZeroWithNewLocalWork_RetainsReceivedProvenance(t *testing.T) {
	s, dir := exchangeFixture(t)
	before := outboundReceiptValue(t, s, dir, "0")
	require.NoError(t, s.store.CreateNode(t.Context(), &model.Node{ID: "TEST-2", Project: "TEST", Seq: 2, Title: "later local", Status: model.StatusOpen, Priority: 3, Weight: 1}))
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.True(t, status.Present)
	require.Empty(t, status.Reason, "current pending work does not change the historical received boundary")
	r, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.NotNil(t, r.Boundary.PendingOutbound)
	require.Zero(t, *r.Boundary.PendingOutbound)
	*r.Boundary.PendingOutbound = 99
	again, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Zero(t, *again.Boundary.PendingOutbound)
	after, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.Equal(t, before, after)
}
