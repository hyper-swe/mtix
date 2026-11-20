// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// publicationStamp is what a publication leaves behind to prove later that
// nothing changed: the change token read before the board was exported, the
// two hashes it wrote, and the stat of the published file (MTIX-130.20.2).
type publicationStamp struct {
	token    string
	fileHash string
	dbHash   string
	info     os.FileInfo
}

// readExportToken reads the change token BEFORE the board is exported. A write
// after this read moves the counter past the recorded token and forces the
// next pass; reading it later could record a token newer than the published
// content and skip a real change forever (MTIX-130.20.2). Failure yields "",
// which never authorizes a skip.
func (s *SyncService) readExportToken(ctx context.Context) string {
	token, err := s.store.ExportChangeToken(ctx)
	if err != nil {
		s.logger.Debug("export change token unavailable; full pass", "error", err)
		return ""
	}
	return token
}

// unchangedSincePublication reports whether the board is provably the one the
// last publication wrote: same token, same build, the published file and both
// hash files exactly as that publication left them. Every doubt is false and
// the caller does the full pass, which stays the source of truth; this never
// returns an error of its own (MTIX-130.20.2).
func (s *SyncService) unchangedSincePublication(ctx context.Context, mtixDir, token string) bool {
	if token == "" {
		return false
	}
	rec, ok, err := s.publishedRecord(ctx)
	if err != nil || !ok || rec.FileHash == "" || rec.DBHash == "" {
		return false
	}
	if rec.Token != token || rec.Build != s.binaryVersion {
		return false
	}
	if !s.publishedBoardIntact(mtixDir, rec) {
		return false
	}
	if !fileHoldsHash(filepath.Join(mtixDir, "data", "sync-db.sha256"), rec.DBHash) {
		return false
	}
	return s.lastExportHashIs(ctx, rec.FileHash)
}

// publishedBoardIntact reports whether tasks.json is the file the last
// publication recorded, and data/sync.sha256 still holds the recorded hash
// (MTIX-130.20.2). Two routes:
//
//   - Trusted record (identity available and sub-second times): size,
//     modification time, change time, inode and device must all equal the
//     record. No board bytes are read; the pulled-board guard uses this to
//     skip reading and hashing tasks.json.
//   - Untrusted record (no change time or file id, as on Windows, or a
//     whole-second time, as on a coarse filesystem): tasks.json is read and
//     hashed once and the hash must equal the recorded file hash. The export
//     is still skipped for an unchanged board.
//
// Residual, by design. A user cannot set the change time, and a replacement by
// rename is another inode, so a tool that keeps the modification time (rsync -t,
// cp -p, touch -r, an archive restore) is seen. What can escape on the trusted
// route is a write that restores size, modification time AND change time: a
// clock change or raw device access, and, on a Linux kernel whose change time
// has the coarse tick granularity of a few milliseconds (before multigrain
// timestamps), an in-place same-size write with the modification time put back
// within the same tick as the publication, or an inode number reused within that
// tick. An in-place rewrite between the rename and the final stat is caught:
// the published bytes are hashed before the record is written. If a foreign
// board did escape, the next export pass that sees changed local content
// publishes over it.
func (s *SyncService) publishedBoardIntact(mtixDir string, rec sqlite.PublishedExport) bool {
	tasksPath := filepath.Join(mtixDir, "tasks.json")
	// #nosec G703 -- fixed tasks.json under the caller-selected local project.
	info, err := os.Stat(tasksPath)
	if err != nil {
		return false
	}
	if _, _, _, ok := s.identityOf(info); !ok || !publishedRecordTrustworthy(rec) {
		if !s.boardHashesTo(tasksPath, rec.FileHash) {
			return false
		}
	} else if !publishedRecordMatchesWith(s.identityOf, info, rec) {
		return false
	}
	return fileHoldsHash(filepath.Join(mtixDir, "data", "sync.sha256"), rec.FileHash)
}

// boardHashesTo reads tasks.json once and reports whether its SHA-256 is want.
func (s *SyncService) boardHashesTo(path, want string) bool {
	// #nosec G703 -- fixed tasks.json under the caller-selected local project.
	data, err := s.publishedReader()(path)
	return err == nil && fmt.Sprintf("%x", sha256.Sum256(data)) == want
}

// identityOf returns the file identity of a stat, through the test seam when
// one is set (nil in production).
func (s *SyncService) identityOf(info os.FileInfo) (int64, uint64, uint64, bool) {
	if s.fileIdentity != nil {
		return s.fileIdentity(info)
	}
	return fileIdentityOf(info)
}

// publishedRecordMatches reports whether a stat of tasks.json equals the
// identity the record holds, field by field (MTIX-130.20.2).
func publishedRecordMatches(info os.FileInfo, rec sqlite.PublishedExport) bool {
	return publishedRecordMatchesWith(fileIdentityOf, info, rec)
}

func publishedRecordMatchesWith(identity func(os.FileInfo) (int64, uint64, uint64, bool), info os.FileInfo, rec sqlite.PublishedExport) bool {
	if !publishedRecordTrustworthy(rec) {
		return false
	}
	ctime, ino, dev, ok := identity(info)
	return ok && info.Size() == rec.Size && info.ModTime().UnixNano() == rec.MtimeNS &&
		ctime == rec.CtimeNS && ino == rec.Ino && dev == rec.Dev
}

// publishedRecordTrustworthy is false for a record that cannot prove identity:
// no change time or file id was available when it was written, or a recorded
// time has a zero sub-second part, the mark of a filesystem coarser than a
// nanosecond, where another file can share the time (MTIX-130.20.2).
func publishedRecordTrustworthy(rec sqlite.PublishedExport) bool {
	const second = int64(time.Second)
	return rec.Ino != 0 && rec.CtimeNS%second != 0 && rec.MtimeNS%second != 0
}

// publishedRecord reads the last publication's record, through the seam a test
// can replace.
func (s *SyncService) publishedRecord(ctx context.Context) (sqlite.PublishedExport, bool, error) {
	if s.readPublishedRecord != nil {
		return s.readPublishedRecord(ctx)
	}
	return s.store.ReadPublishedExport(ctx)
}

// finalPublishedStat stats tasks.json after the rename and the directory sync
// (a rename moves the change time) and returns that stat only when it is the
// very file this pass wrote: same file, size and modification time as the
// temporary file before the rename, and bytes whose SHA-256 is fileHash. The
// bytes are always hashed once here, because a publication has just written the
// file anyway and the check closes any in-place rewrite made between the rename
// and this stat. Otherwise nil, and no record is written (MTIX-130.20.2).
func finalPublishedStat(path string, written os.FileInfo, fileHash string) os.FileInfo {
	if written == nil {
		return nil
	}
	// #nosec G703 -- fixed tasks.json under the caller-selected local project.
	final, err := os.Stat(path)
	if err != nil || !os.SameFile(written, final) || final.Size() != written.Size() || !final.ModTime().Equal(written.ModTime()) {
		return nil
	}
	// #nosec G304 G703 -- fixed tasks.json under the caller-selected local project.
	data, err := os.ReadFile(path)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != fileHash {
		return nil
	}
	return final
}

// publishedBoardHash returns the file hash the last publication recorded when
// tasks.json is provably that publication's file (publishedBoardIntact), and
// "" otherwise. Any read error or missing record is "" and the caller reads and
// hashes the file (MTIX-130.20.2).
func (s *SyncService) publishedBoardHash(ctx context.Context, mtixDir string) string {
	rec, ok, err := s.publishedRecord(ctx)
	if err != nil || !ok || rec.FileHash == "" || !s.publishedBoardIntact(mtixDir, rec) {
		return ""
	}
	return rec.FileHash
}

// fileHoldsHash reports whether the small hash file holds exactly want.
func fileHoldsHash(path, want string) bool {
	// #nosec G304 G703 -- fixed hash file name under the caller-selected local project.
	raw, err := os.ReadFile(path)
	return err == nil && string(raw) == want
}

// lastExportHashIs reports whether meta.last_export_hash equals want.
func (s *SyncService) lastExportHashIs(ctx context.Context, want string) bool {
	var got string
	// The hash the last publication recorded for sibling agents' import skip.
	err := s.store.ReadDB().QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'last_export_hash'").Scan(&got)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return false
	}
	return got == want
}

// recordPublication stores the stamp of a finished publication. A failure only
// costs the next pass its skip, so it is logged and never fails the export.
func (s *SyncService) recordPublication(ctx context.Context, st publicationStamp) {
	if st.token == "" || st.info == nil {
		return
	}
	ctime, ino, dev, _ := s.identityOf(st.info) // zeros when unavailable: the record is then never trusted
	err := s.store.WritePublishedExport(ctx, sqlite.PublishedExport{
		Token: st.token, Build: s.binaryVersion, FileHash: st.fileHash, DBHash: st.dbHash,
		Size: st.info.Size(), MtimeNS: st.info.ModTime().UnixNano(),
		CtimeNS: ctime, Ino: ino, Dev: dev,
	})
	if err != nil {
		s.logger.Debug("published export record not written; next pass is full", "error", err)
	}
}
