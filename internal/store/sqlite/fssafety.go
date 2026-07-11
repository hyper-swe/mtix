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

// preflightFilesystem is the MTIX-54 open-time gate. It classifies the database
// filesystem and either (a) returns nil,nil on a safe FS, (b) refuses with a
// wrapped errUnsafeFilesystem on an unsafe FS the operator has not opted into,
// or (c) returns safeMode=true (open non-WAL) on an unsafe FS the operator has
// explicitly allowed. A classification error fails OPEN (proceed in WAL) so the
// preflight itself never blocks opening on a transient statfs failure.
func preflightFilesystem(dbPath string, logger *slog.Logger) (safeMode bool, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	dir := filepath.Dir(dbPath)
	fsType, class, derr := fsDetector(dir)
	if derr != nil {
		logger.Warn("filesystem-safety preflight: could not classify filesystem; proceeding in WAL mode",
			"dir", dir, "error", derr)
		return false, nil
	}
	if !class.unsafe() {
		return false, nil
	}
	if serr := filesystemSafetyError(dbPath, fsType, class, allowUnsafeFS()); serr != nil {
		return false, serr
	}
	logger.Warn("mtix database is on an UNSAFE filesystem; opening in non-WAL safe mode ("+
		allowUnsafeFSEnv+" set). Corruption risk remains — prefer a local disk or the sync hub.",
		"fs", fsType, "class", class.String(), "path", dbPath)
	return true, nil
}

// Filesystem-safety preflight (MTIX-54). SQLite WAL mode keeps its wal-index in
// a shared-memory (-shm) file mmap'd into every accessing process, and relies on
// faithful POSIX byte-range locking + fsync. FUSE-passthrough and network
// filesystems do not provide true cross-process shared memory or reliable
// locking (while often reporting success), so concurrent processes run on
// divergent wal-index views and corrupt the database — the 2026-07-11 incident.
// The preflight refuses to open on a positively-identified unsafe filesystem
// unless the operator opts in, in which case the store opens in a non-WAL mode.

// allowUnsafeFSEnv opts into opening on an unsafe filesystem (in non-WAL safe
// mode). Deliberately explicit and per-operator — never inferred.
const allowUnsafeFSEnv = "MTIX_ALLOW_UNSAFE_FS"

// errUnsafeFilesystem is the sentinel wrapped by the refuse-to-open error, so
// callers/tests can match it without parsing the message.
var errUnsafeFilesystem = errors.New("mtix database is on a filesystem unsafe for SQLite WAL")

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

// allowUnsafeFS reports whether the operator has opted into opening on an unsafe
// filesystem (non-WAL safe mode).
func allowUnsafeFS() bool {
	v := strings.TrimSpace(os.Getenv(allowUnsafeFSEnv))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// filesystemSafetyError returns an actionable, wrapped error when the database
// filesystem is unsafe for WAL and the operator has not opted in; nil otherwise.
func filesystemSafetyError(dbPath, fsType string, class fsClass, allowUnsafe bool) error {
	if !class.unsafe() || allowUnsafe {
		return nil
	}
	return fmt.Errorf(
		"%w: %s is on a %s filesystem (%q). SQLite cannot guarantee integrity there — "+
			"WAL shared-memory and POSIX locking are unreliable on FUSE/network mounts, "+
			"which risks database corruption. Move .mtix onto a local disk; for a "+
			"sandboxed or multi-agent setup, give each machine its own local .mtix and "+
			"share state via the sync hub. To override at your own risk (opens in a "+
			"slower, non-WAL single-writer mode), set %s=1",
		errUnsafeFilesystem, dbPath, class, fsType, allowUnsafeFSEnv)
}
