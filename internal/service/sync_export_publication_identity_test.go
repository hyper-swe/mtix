// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// publishedFixture is a fixture whose first publication has been recorded, with
// the record and the stat of the published file at that moment.
func publishedFixture(t *testing.T) (*marshalFixture, sqlite.PublishedExport, string) {
	t.Helper()
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	rec, ok, err := f.st.ReadPublishedExport(ctx)
	require.NoError(t, err)
	require.True(t, ok, "the first publication must record itself")
	path := filepath.Join(f.dir, "tasks.json")
	info, err := os.Stat(path)
	require.NoError(t, err)
	if _, _, _, hasIdentity := fileIdentityOf(info); !hasIdentity {
		t.Skip("no file identity on this platform; the no-identity tests cover the hashing route")
	}
	return f, rec, path
}

// requireForeignBoardKept runs one local edit and an automatic export and
// asserts the foreign tasks.json bytes are still on disk: refused or kept, never
// silently overwritten (MTIX-130.20.2).
func requireForeignBoardKept(t *testing.T, f *marshalFixture, path string, foreign []byte) {
	t.Helper()
	f.svc.SetNoticeWriter(io.Discard)
	f.rename(t)
	_ = f.svc.AutoExport(context.Background(), f.dir)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(foreign), string(got), "a foreign board must never be silently overwritten")
}

// A same-size foreign board renamed over tasks.json with the recorded
// modification time put back is another inode with a new change time: the guard
// hashes it and refuses (MTIX-130.20.2, the reviewer's probe).
func TestAutoExport_SameSizeSameMtimeBoardRenamedIn_IsNotOverwritten(t *testing.T) {
	f, rec, path := publishedFixture(t)
	foreign := []byte(strings.Repeat("x", int(rec.Size)))
	tmp := filepath.Join(f.dir, "incoming.tmp")
	require.NoError(t, os.WriteFile(tmp, foreign, 0o644))
	mtime := time.Unix(0, rec.MtimeNS)
	require.NoError(t, os.Chtimes(tmp, mtime, mtime))
	require.NoError(t, os.Rename(tmp, path))
	requireForeignBoardKept(t, f, path, foreign)
}

// An in-place same-size overwrite with the modification time restored still
// moves the change time, which no tool can set back (MTIX-130.20.2).
func TestAutoExport_SameSizeInPlaceOverwriteMtimeRestored_IsNotOverwritten(t *testing.T) {
	f, rec, path := publishedFixture(t)
	foreign := []byte(strings.Repeat("y", int(rec.Size)))
	require.NoError(t, os.WriteFile(path, foreign, 0o644))
	mtime := time.Unix(0, rec.MtimeNS)
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	requireForeignBoardKept(t, f, path, foreign)
}

// The record matches a stat only when every identity field is equal, so each
// row moves exactly one field of the record (MTIX-130.20.2).
func TestPublishedRecordMatches_EachFieldMustEqual(t *testing.T) {
	_, rec, path := publishedFixture(t)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, publishedRecordMatches(info, rec), "the recorded file matches its own record")
	cases := []struct {
		name string
		edit func(r *sqlite.PublishedExport)
	}{
		{"different size, same mtime", func(r *sqlite.PublishedExport) { r.Size++ }},
		{"mtime moved by nanoseconds", func(r *sqlite.PublishedExport) { r.MtimeNS += 3 }},
		{"ctime moved by nanoseconds", func(r *sqlite.PublishedExport) { r.CtimeNS += 3 }},
		{"another inode", func(r *sqlite.PublishedExport) { r.Ino++ }},
		{"another device", func(r *sqlite.PublishedExport) { r.Dev++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edited := rec
			tc.edit(&edited)
			require.False(t, publishedRecordMatches(info, edited))
		})
	}
}

// A record with no file id or a whole-second time cannot prove identity, and a
// file whose mtime has no sub-second part is never trusted even when its record
// equals its stat (MTIX-130.20.2).
func TestPublishedRecordTrustworthy_RefusesCoarseOrMissingIdentity(t *testing.T) {
	_, rec, path := publishedFixture(t)
	require.True(t, publishedRecordTrustworthy(rec))
	cases := []struct {
		name string
		edit func(r *sqlite.PublishedExport)
	}{
		{"no inode", func(r *sqlite.PublishedExport) { r.Ino = 0 }},
		{"whole-second mtime", func(r *sqlite.PublishedExport) { r.MtimeNS = (r.MtimeNS/int64(time.Second) + 1) * int64(time.Second) }},
		{"whole-second ctime", func(r *sqlite.PublishedExport) { r.CtimeNS = (r.CtimeNS/int64(time.Second) + 1) * int64(time.Second) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edited := rec
			tc.edit(&edited)
			require.False(t, publishedRecordTrustworthy(edited))
		})
	}
	whole := time.Unix(1_700_000_000, 0)
	require.NoError(t, os.Chtimes(path, whole, whole))
	info, err := os.Stat(path)
	require.NoError(t, err)
	ctime, ino, dev, ok := fileIdentityOf(info)
	if !ok {
		t.Skip("no file identity on this platform; the record is never trusted there")
	}
	coarse := sqlite.PublishedExport{Size: info.Size(), MtimeNS: info.ModTime().UnixNano(), CtimeNS: ctime, Ino: ino, Dev: dev}
	require.False(t, publishedRecordMatches(info, coarse), "a whole-second mtime is never trusted")
}

// A failing record read is a doubt: one full pass, no error, and the board is
// recorded again afterwards (MTIX-130.20.2).
func TestAutoExport_RecordReadFails_FullPassNoError(t *testing.T) {
	f, _, _ := publishedFixture(t)
	ctx := context.Background()
	c := countPassWork(f)
	f.svc.readPublishedRecord = func(context.Context) (sqlite.PublishedExport, bool, error) {
		return sqlite.PublishedExport{}, false, errors.New("record read failed")
	}
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Equal(t, 1, c.exports, "a failing record read must give one full pass")
	require.Positive(t, c.reads, "and the guard must hash the board")
	require.False(t, f.svc.unchangedSincePublication(ctx, f.dir, f.svc.readExportToken(ctx)))
}

// A record written by an earlier build (revision 1, no identity) never matches
// and forces one full pass (MTIX-130.20.2).
func TestAutoExport_EarlierRecordRevision_ForcesFullPass(t *testing.T) {
	f, _, _ := publishedFixture(t)
	ctx := context.Background()
	c := countPassWork(f)
	execWrite(t, f, `UPDATE meta SET value = json_set(value, '$.v', 1) WHERE key = 'export.published_state'`)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Equal(t, 1, c.exports)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Equal(t, 1, c.exports, "the full pass records the board again")
}

// noIdentity is the identity seam of a platform with no change time or file id.
func noIdentity(os.FileInfo) (int64, uint64, uint64, bool) { return 0, 0, 0, false }

// Where no file identity is available the record is never trusted, so the
// pulled-board check and the skip decision read and hash tasks.json, but an
// unchanged board still skips the store export (MTIX-130.20.2).
func TestAutoExport_NoFileIdentity_HashesBoardAndStillSkipsExport(t *testing.T) {
	f := newMarshalFixture(t)
	f.svc.fileIdentity = noIdentity
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	rec, ok, err := f.st.ReadPublishedExport(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Zero(t, rec.Ino, "no identity must be recorded when none is available")
	c := countPassWork(f)
	for tick := 0; tick < 3; tick++ {
		*f.now = f.now.Add(time.Minute)
		require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	}
	require.Zero(t, c.exports, "an unchanged board must not be exported again")
	require.Positive(t, c.reads, "without identity the board must be read and hashed")
	status, err := f.svc.PendingExports(ctx, f.dir)
	require.NoError(t, err)
	require.Zero(t, status.Count)
}

// Without identity, one changed byte of the same size is seen by the hash: no
// skip, and the foreign board is kept (MTIX-130.20.2).
func TestAutoExport_NoFileIdentity_ChangedByteForcesFullPass(t *testing.T) {
	f := newMarshalFixture(t)
	f.svc.fileIdentity = noIdentity
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	path := filepath.Join(f.dir, "tasks.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, f.svc.unchangedSincePublication(ctx, f.dir, f.svc.readExportToken(ctx)), "the untouched board is skipped")
	foreign := append([]byte(nil), raw...)
	foreign[0] ^= 0x01 // the opening brace becomes a letter: not a valid board
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, foreign, 0o644))
	require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))
	require.False(t, f.svc.unchangedSincePublication(ctx, f.dir, f.svc.readExportToken(ctx)), "a changed byte must force the full pass")
	requireForeignBoardKept(t, f, path, foreign)
}

// The published bytes are hashed before the record is written, so a file that
// is not the one this pass wrote gets no record (MTIX-130.20.2).
func TestFinalPublishedStat_BytesNotTheWrittenOnes_NoStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	require.NoError(t, os.WriteFile(path, []byte("abcd"), 0o644))
	info, err := os.Stat(path)
	require.NoError(t, err)
	const abcdHash = "88d4266fd4e6338d13b845fcf289579d209c897823b9217da3e161936f031589"
	require.NotNil(t, finalPublishedStat(path, info, abcdHash))
	require.Nil(t, finalPublishedStat(path, info, "00"), "other bytes than the hashed ones get no record")
	require.Nil(t, finalPublishedStat(path, nil, abcdHash))
	require.Nil(t, finalPublishedStat(filepath.Join(filepath.Dir(path), "missing"), info, abcdHash))
}
