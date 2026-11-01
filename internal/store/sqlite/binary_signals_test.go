// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestBinarySignals_ListsRegistrationsAndDaemonsOfEveryHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	got, err := s.BinarySignals(t.Context())
	require.NoError(t, err)
	require.Empty(t, got)

	db := newTestDB(t, path)
	for _, q := range []string{
		"INSERT INTO agent_registrations(agent_id,harness,uid,host_id,origin,registered_at,last_seen,registrar_version,attach_protocol,returned_seq_mark) VALUES('w1','local','u1','hostA','manual','t0','2026-10-01T00:00:00Z','0.5.9',1,0)",
		"INSERT INTO agent_registrations(agent_id,harness,uid,host_id,origin,registered_at,last_seen,registrar_version,attach_protocol,returned_seq_mark) VALUES('w2','local','u2','hostB','manual','t0','2026-10-02T00:00:00Z','0.6.1',1,0)",
		"INSERT INTO dispatcher_heartbeat(host_id,uid,pid,protocol,version,seen_at,capabilities) VALUES('hostC','u3',77,1,'0.5.8','2026-10-03T00:00:00Z','history_export_reader')",
	} {
		_, err := db.Exec(q)
		require.NoError(t, err)
	}
	got, err = s.BinarySignals(t.Context())
	require.NoError(t, err)
	require.ElementsMatch(t, []sqlite.BinarySignal{
		{Source: "registration", Name: "w1", Version: "0.5.9", SeenAt: "2026-10-01T00:00:00Z"},
		{Source: "registration", Name: "w2", Version: "0.6.1", SeenAt: "2026-10-02T00:00:00Z"},
		{Source: "daemon", Name: "hostC:77", Version: "0.5.8", Capabilities: sqlite.HistoryReaderCapability, SeenAt: "2026-10-03T00:00:00Z"},
	}, got)
}

func newSignalsStore(t *testing.T) (*sqlite.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	return s, path
}

// Every registration and heartbeat this build writes carries the marker, so a
// build that holds task history is never read as an older reader, whatever
// its version string says (git-describe stamps on main).
func TestBinarySignals_RegistrationAndHeartbeatWrittenByThisBuild_CarryTheMarker(t *testing.T) {
	s, _ := newSignalsStore(t)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := t.Context()
	key := sqlite.RegistrationKey{AgentID: "me", HostID: "h", UID: "u"}
	_, err := s.RegisterSessionAtomic(ctx, key, func(_ *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{AgentID: "me", HostID: "h", UID: "u", Harness: "local", Origin: "manual", Epoch: 1,
			RegisteredAt: "2026-10-01T00:00:00Z", LastSeen: "2026-10-01T00:00:00Z", RegistrarVersion: "v0.5.4-beta-312-g7ed998a8-dirty", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	require.NoError(t, s.RecordDispatcherHeartbeat(ctx, sqlite.DispatcherHeartbeat{HostID: "h", UID: "u", PID: 5, Protocol: 1,
		Version: "v0.5.4-beta-312-g7ed998a8-dirty", SeenAt: "2026-10-01T00:00:00Z"}))
	got, err := s.BinarySignals(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, sig := range got {
		require.Equal(t, sqlite.HistoryReaderCapability, sig.Capabilities, sig.Source)
	}
}

// A board created before the column existed gains it, empty, without losing
// rows: those rows were written by builds without the marker.
func TestBinarySignals_BoardWithoutTheColumn_GainsItAndOldRowsHaveNoMarker(t *testing.T) {
	s, path := newSignalsStore(t)
	db := newTestDB(t, path)
	for _, q := range []string{
		"INSERT INTO agent_registrations(agent_id,harness,uid,host_id,origin,registered_at,last_seen,registrar_version,attach_protocol,returned_seq_mark,capabilities) VALUES('w1','local','u1','hostA','manual','t0','2026-10-01T00:00:00Z','0.6.0',1,0,'history_export_reader')",
		"ALTER TABLE agent_registrations DROP COLUMN capabilities",
		"ALTER TABLE dispatcher_heartbeat DROP COLUMN capabilities",
	} {
		_, err := db.Exec(q)
		require.NoError(t, err, q)
	}
	require.NoError(t, s.Close())
	s2, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s2.Close()) })
	got, err := s2.BinarySignals(t.Context())
	require.NoError(t, err)
	require.Equal(t, []sqlite.BinarySignal{{Source: "registration", Name: "w1", Version: "0.6.0", SeenAt: "2026-10-01T00:00:00Z"}}, got)
}
