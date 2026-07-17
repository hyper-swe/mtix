// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// fsDetector classifies the filesystem holding a directory. It is a var so tests
// can inject an unsafe classification without a real FUSE/network mount.
var fsDetector = detectFS

// Filesystem-safety preflight (MTIX-54, hardened by MTIX-58). SQLite WAL keeps
// its wal-index in a shared-memory (-shm) file mmap'd into every accessing
// process and relies on faithful POSIX locking + fsync. FUSE-passthrough and
// network filesystems provide neither reliably (while reporting success), so
// concurrent WRITERS corrupt the database — the 2026-07-11/12 incidents, BOTH
// via the old MTIX_ALLOW_UNSAFE_FS write override. That override is retired:
// on an unsafe filesystem the store opens READ-ONLY (so recover/query/export
// still work) and every WRITE is refused, with no way to override it.

// preflightFilesystem classifies the database filesystem and reports whether
// WRITES must be refused. On a positively-identified unsafe (FUSE/network)
// filesystem it returns writeRefused=true (the store opens read-only). A safe FS
// — or a classification error (fail open) — returns false (normal read+write in
// WAL). It never blocks the OPEN itself; reads are always permitted.
func preflightFilesystem(dbPath string, logger *slog.Logger) (writeRefused bool, fsType string) {
	if logger == nil {
		logger = slog.Default()
	}
	dir := filepath.Dir(dbPath)
	fsType, class, derr := fsDetector(dir)
	if derr != nil {
		logger.Warn("filesystem-safety preflight: could not classify filesystem; proceeding in WAL mode",
			"dir", dir, "error", derr)
		return false, ""
	}
	if !class.unsafe() {
		return false, fsType
	}
	logger.Warn("mtix database is on an UNSAFE filesystem — opening READ-ONLY; WRITES are refused. "+
		"Move .mtix to a local disk, or give each machine its own local .mtix and sync via the hub.",
		"fs", fsType, "class", class.String(), "path", dbPath)
	if _, set := os.LookupEnv(allowUnsafeFSEnv); set {
		logger.Warn("deprecated: " + allowUnsafeFSEnv + " no longer enables writes on an unsafe " +
			"filesystem (retired after two override-write corruptions) — it is ignored.")
	}
	return true, fsType
}

// allowUnsafeFSEnv is the RETIRED write override. Recognized only to emit a
// deprecation warning; it never enables a write on an unsafe filesystem (MTIX-58).
const allowUnsafeFSEnv = "MTIX_ALLOW_UNSAFE_FS"

// errUnsafeFilesystem is the sentinel wrapped by the write-refusal error, so
// callers/tests can match it without parsing the message.
var errUnsafeFilesystem = errors.New("writes refused: mtix database is on a filesystem unsafe for SQLite")

// writeRefusedError is returned for every write attempt on an unsafe filesystem.
func writeRefusedError(dbPath, fsType string) error {
	return fmt.Errorf("%w (%q, at %s) — SQLite cannot write there safely; WAL shared-memory and "+
		"POSIX locking are unreliable on FUSE/network mounts, which risks corruption. Reads, "+
		"'mtix recover', and 'mtix export' still work; to WRITE, move .mtix to a local disk or use "+
		"the sync hub. No environment override enables writes here",
		errUnsafeFilesystem, fsType, dbPath)
}

// fsClass categorizes the filesystem holding the database.
type fsClass int

const (
	fsLocal   fsClass = iota // real local disk — WAL is safe
	fsFuse                   // FUSE passthrough — shared-memory/locking unreliable
	fsNetwork                // NFS/SMB/9p/etc — shared-memory/locking unreliable
)

// unsafe reports whether SQLite WAL cannot be trusted on this class.
func (c fsClass) unsafe() bool { return c == fsFuse || c == fsNetwork }

func (c fsClass) String() string {
	switch c {
	case fsFuse:
		return "FUSE"
	case fsNetwork:
		return "network"
	default:
		return "local"
	}
}

// networkFSNames are filesystem type names that are network/shared and unsafe
// for WAL. FUSE-backed userspace filesystems are handled separately.
var networkFSNames = map[string]bool{
	"nfs": true, "nfs4": true, "smb": true, "smb2": true, "smbfs": true,
	"cifs": true, "afpfs": true, "webdav": true, "ftp": true, "9p": true,
	"v9fs": true, "ncpfs": true, "afs": true, "ceph": true,
	"glusterfs": true, "gluster": true,
}

// fuseBackedNames are common FUSE userspace filesystems whose type name does not
// literally contain "fuse".
var fuseBackedNames = map[string]bool{
	"sshfs": true, "rclone": true, "gocryptfs": true, "s3fs": true,
	"bindfs": true, "curlftpfs": true, "mergerfs": true, "encfs": true,
	"gvfs": true, "gvfsd-fuse": true,
}

// classifyFSName maps a filesystem type name (statfs f_fstypename on darwin/BSD,
// or the name mapped from the Linux f_type magic) to a class. Unknown/empty
// names are LOCAL (safe): the preflight refuses only on positively-identified
// FUSE/network filesystems, so an unrecognized-but-fine local FS never trips it.
func classifyFSName(name string) fsClass {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case n == "":
		return fsLocal
	case strings.Contains(n, "fuse"): // fuse, macfuse, osxfuse, fuseblk, fuse.<x>
		return fsFuse
	case fuseBackedNames[n]:
		return fsFuse
	case networkFSNames[n]:
		return fsNetwork
	default:
		return fsLocal
	}
}

// linuxFSMagicName maps a Linux statfs f_type magic (linux/magic.h) to a
// filesystem name; "" for unrecognized magics (treated as local). Plain numbers,
// so the mapping is unit-tested on any OS.
func linuxFSMagicName(magic int64) string {
	switch magic {
	case 0x65735546: // FUSE_SUPER_MAGIC
		return "fuse"
	case 0x6969: // NFS_SUPER_MAGIC
		return "nfs"
	case 0x517b: // SMB_SUPER_MAGIC
		return "smb"
	case 0xff534d42: // CIFS_MAGIC_NUMBER
		return "cifs"
	case 0xfe534d42: // SMB2_MAGIC_NUMBER
		return "smb2"
	case 0x01021997: // V9FS_MAGIC (9p)
		return "9p"
	case 0x00c36400: // CEPH_SUPER_MAGIC
		return "ceph"
	// Local filesystems named for observability; all classify safe.
	case 0xef53: // EXT2/3/4
		return "ext"
	case 0x58465342: // XFS
		return "xfs"
	case 0x9123683e: // BTRFS
		return "btrfs"
	case 0x01021994: // TMPFS
		return "tmpfs"
	case 0x2fc12fc1: // ZFS
		return "zfs"
	case 0x794c7630: // OVERLAYFS
		return "overlay"
	default:
		return ""
	}
}

// (allowUnsafeFS and filesystemSafetyError were retired in MTIX-58: the write
// override is gone, so there is no "allow" path and no open-refusal error —
// unsafe filesystems open read-only and refuse writes via writeRefusedError.)
