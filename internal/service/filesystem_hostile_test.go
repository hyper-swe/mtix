// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Filesystem guarantees for hostile persisted identifiers and ticket text (MTIX-187.7.7).
package service_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type hostileFileWriter struct {
	name, pattern string
	write         func(*testing.T, string, string)
}

func TestHostileIDs_NeverCreateFilesOutsideBaseDir(t *testing.T) {
	runHostileFileWriters(t, false)
}

func TestHostileIDs_NeverCreateSurpriseFilenames(t *testing.T) {
	runHostileFileWriters(t, true)
}

func runHostileFileWriters(t *testing.T, names bool) {
	t.Helper()
	writers := []hostileFileWriter{
		{"relay", `^seg-[0-9]{8,}\.mrseg$`, writeHostileSegment},
		{"keyring", `^1$`, writeHostileKey},
		{"presync", `^(board\.db(?:-wal|-shm)?|data|backups|pre-sync-20261028-120000\.db)$`, writeHostilePreSync},
		{"rolling-backup", `^(board\.db(?:-wal|-shm)?|mtix-20261028-120000\.db)$`, writeHostileRollingBackup},
		{"backup-user-path", `^(board\.db(?:-wal|-shm)?|chosen\.db)$`, writeHostileBackup},
		{"relay-bootstrap", `^(board\.db(?:-wal|-shm)?|data|relay|bootstrap|snap-20261028120000-0123456789abcdef\.mrsnap)$`, writeHostileBootstrap},
		{"export", `^(board\.db(?:-wal|-shm)?|data|tasks\.json|sync\.sha256|sync-db\.sha256|sync-base\.json|exchange-record\.json|sync\.lock|sync-export\.lock)$`, writeHostileExport},
	}
	fixtures := append(testutil.HostileIDs(), testutil.HostileText()...)
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			cases := fixtures
			if w.name != "relay" && w.name != "keyring" {
				cases = []testutil.HostileFixture{{Name: "all-fixtures"}}
			}
			for _, f := range cases {
				t.Run(f.Name, func(t *testing.T) {
					root := t.TempDir()
					base := filepath.Join(root, "sandbox", "base")
					require.NoError(t, os.MkdirAll(base, 0o700))
					w.write(t, base, f.Value)
					assertHostileTree(t, root, base, w.pattern, names)
				})
			}
		})
	}
}

func assertHostileTree(t *testing.T, root, base, pattern string, names bool) {
	t.Helper()
	allowed := regexp.MustCompile(pattern)
	count := 0
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		require.NoError(t, err)
		if path == root || path == filepath.Dir(base) || path == base {
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		require.NoError(t, relErr)
		require.False(t, rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)), "outside base: %q", path)
		require.Zero(t, entry.Type()&os.ModeSymlink, "unexpected symlink: %q", path)
		if names {
			require.True(t, allowed.MatchString(entry.Name()), "surprise filename: %q", rel)
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	}))
	require.Positive(t, count, "writer must actually produce files")
}

func writeHostileSegment(t *testing.T, base, value string) {
	t.Helper()
	w, err := segment.NewWriter(segment.WriterConfig{Dir: base, PeerID: "0123456789abcdef", Unauthenticated: true, PubEpoch: 1})
	require.NoError(t, err)

	for _, field := range []string{"event_id", "title"} {
		// One field per frame keeps the >64 KiB fixture below the relay frame cap.
		payload, marshalErr := json.Marshal(map[string]string{field: value})
		require.NoError(t, marshalErr)
		_, appendErr := w.Append(payload)
		require.NoError(t, appendErr)
	}

	require.NoError(t, w.Close())
	// Opaque event IDs must also never be accepted as relay peer directories.
	_, err = segment.NewWriter(segment.WriterConfig{Dir: base, PeerID: value, Unauthenticated: true, PubEpoch: 1})
	require.Error(t, err)
}

func writeHostileKey(t *testing.T, base, value string) {
	t.Helper()
	// Epochs are numeric, never event IDs. Hostile bytes can only be key material.
	key := append([]byte(value), bytes.Repeat([]byte("k"), keyring.MinKeyBytes)...)
	require.NoError(t, keyring.Write(base, 1, key))
	ring, err := keyring.Load(base)
	require.NoError(t, err)
	got, err := ring.For(1)
	require.NoError(t, err)
	require.Equal(t, key, got)
}

func hostileFileStore(t *testing.T, base, _ string) *sqlite.Store {
	t.Helper()
	st, err := sqlite.New(filepath.Join(base, "board.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	seedHostileFileStore(t, st)

	return st
}

func hostileFileSync(st *sqlite.Store) *service.SyncService {
	return service.NewSyncService(st, slog.Default(), func() time.Time {
		return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	})
}

func writeHostilePreSync(t *testing.T, base, value string) {
	t.Helper()
	st := hostileFileStore(t, base, value)
	path, err := hostileFileSync(st).BackupBeforeImport(t.Context(), base)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(base, "data", "backups", "pre-sync-20261028-120000.db"), path)
}

func writeHostileBackup(t *testing.T, base, value string) {
	t.Helper()
	st := hostileFileStore(t, base, value)
	dest := filepath.Join(base, "chosen.db")
	result, err := service.NewAdminService(st).Backup(t.Context(), dest)
	require.NoError(t, err)
	require.Equal(t, dest, result.Path)
	require.True(t, result.Verified)
}

func writeHostileBootstrap(t *testing.T, base, value string) {
	t.Helper()
	st := hostileFileStore(t, base, value)
	relay := filepath.Join(base, "relay")
	res, err := bootstrap.ExportSnapshot(t.Context(), bootstrap.ExportRequest{
		Store: st, RelayDir: relay, Project: "TEST", ExportedBy: "0123456789abcdef",
		CreatedAt: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
		Positions: map[string]uint64{"0123456789abcdef": 1},
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(relay, "bootstrap", "snap-20261028120000-0123456789abcdef.mrsnap"), res.Path)
	// The snapshot body carries the hostile ids and titles as JSON data only.
	raw, err := os.ReadFile(res.Path)
	require.NoError(t, err)
	require.True(t, json.Valid(raw))
	// A hostile exporting peer id is refused before any file name is built.
	_, err = bootstrap.ExportSnapshot(t.Context(), bootstrap.ExportRequest{
		Store: st, RelayDir: relay, Project: "TEST", ExportedBy: "x" + value,
		CreatedAt: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
	})
	require.Error(t, err)
}

func writeHostileExport(t *testing.T, base, value string) {
	t.Helper()
	st := hostileFileStore(t, base, value)
	require.NoError(t, os.Mkdir(filepath.Join(base, "data"), 0o700))
	require.NoError(t, hostileFileSync(st).AutoExport(t.Context(), base))
	raw, err := os.ReadFile(filepath.Join(base, "tasks.json"))
	require.NoError(t, err)
	require.True(t, json.Valid(raw))
}

func writeHostileRollingBackup(t *testing.T, base, value string) {
	t.Helper()
	st := hostileFileStore(t, base, value)
	scheduler := service.NewBackupScheduler(st, base, time.Hour, 5, slog.Default(), func() time.Time {
		return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	})
	path, err := scheduler.MaybeBackup(t.Context())
	require.NoError(t, err)
	require.Equal(t, filepath.Join(base, "mtix-20261028-120000.db"), path)
}

func seedHostileFileStore(t *testing.T, st *sqlite.Store) {
	t.Helper()
	// Seed persisted historical bytes directly: local creation validation is
	// outside this guarantee, and a single transaction keeps this guard cheap.
	require.NoError(t, st.WithTx(t.Context(), func(tx *sql.Tx) error {
		seen := map[string]bool{}
		for _, f := range append(testutil.HostileIDs(), testutil.HostileText()...) {
			if seen[f.Value] {
				continue
			}
			seen[f.Value] = true
			_, err := tx.ExecContext(t.Context(),
				"INSERT INTO nodes (id, depth, seq, project, title, node_type, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
				f.Value, 0, len(seen), "TEST", f.Value, "epic", "2026-10-28T12:00:00Z", "2026-10-28T12:00:00Z")
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(t.Context(),
				"INSERT INTO sync_events (event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,sync_status,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
				f.Value, "TEST", f.Value, "comment", `{}`, 1, len(seen), `{}`, "test-agent", "0123456789abcdef", "pending", "2026-10-28T12:00:00Z")
			if err != nil {
				return err
			}
		}
		return nil
	}))
}
