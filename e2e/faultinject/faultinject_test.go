// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build faultinject

// Package faultinject is the NFR-2.8 conformance suite: it proves, on a
// real filesystem driven to exhaustion and with processes killed
// mid-write, that mtix either completes safely or fail-stops — and never
// silently corrupts the database. This is the evidence behind every
// robustness claim QUALITY-STANDARDS.md makes; it runs on every CI build
// (see .github/workflows/ci.yml, job test-fault-injection).
//
// The suite needs a small dedicated volume so it can fill the disk
// without harming the host:
//
//	Linux:  sudo mount -t tmpfs -o size=16m,mode=1777 tmpfs /mnt/mtix-faultfs
//	macOS:  scripts/faultfs.sh create
//
// then: MTIX_FAULTFS_DIR=<mountpoint> go test ./e2e/faultinject/ -tags=faultinject
package faultinject

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// mtixBin is the path of the binary built by TestMain. Tests exercise the
// real executable — the same artifact users run — not in-process calls.
var mtixBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "mtix-faultinject-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdir temp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	mtixBin = filepath.Join(tmp, "mtix")
	build := exec.Command("go", "build", "-o", mtixBin, "github.com/hyper-swe/mtix/cmd/mtix")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build mtix:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// faultFSDir returns the dedicated small volume, skipping the test when
// none is configured (local runs without the harness).
func faultFSDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("MTIX_FAULTFS_DIR")
	if dir == "" {
		t.Skip("MTIX_FAULTFS_DIR not set; see package comment for harness setup")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("MTIX_FAULTFS_DIR %q is not a usable directory: %v", dir, err)
	}
	return dir
}

// newProject initializes an mtix project in a fresh subdirectory of base.
func newProject(t *testing.T, base string) string {
	t.Helper()
	proj, err := os.MkdirTemp(base, "proj")
	if err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(proj) })

	out, err := runMtix(proj, nil, "init", "--prefix", "FI")
	if err != nil {
		t.Fatalf("mtix init: %v\n%s", err, out)
	}
	return proj
}

// runMtix executes the built binary in dir with extra env, returning
// combined output.
func runMtix(dir string, extraEnv []string, args ...string) ([]byte, error) {
	cmd := exec.Command(mtixBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd.CombinedOutput()
}

// containsAny reports whether data contains at least one of the keywords,
// case-insensitively.
func containsAny(data []byte, keywords ...string) bool {
	low := bytes.ToLower(data)
	for _, kw := range keywords {
		if bytes.Contains(low, []byte(kw)) {
			return true
		}
	}
	return false
}

// Exit codes from the MTIX-26.8 CLI contract (cmd/mtix/exitcode.go).
const (
	exitDiskFull  = 3
	exitCorrupted = 4
)

// exitCode extracts the process exit code from runMtix's error.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// fillDisk writes ballast into dir until the volume returns ENOSPC,
// returning a cleanup func that frees the space. Chunks shrink from 1 MiB
// to 4 KiB so the volume ends up packed tight.
func fillDisk(t *testing.T, dir string) (cleanup func()) {
	t.Helper()
	ballastDir, err := os.MkdirTemp(dir, "ballast")
	if err != nil {
		t.Fatalf("mkdir ballast: %v", err)
	}
	cleanup = func() { _ = os.RemoveAll(ballastDir) }

	chunkSizes := []int{1 << 20, 64 << 10, 4 << 10}
	i := 0
	for _, size := range chunkSizes {
		chunk := bytes.Repeat([]byte{0xAB}, size)
		for {
			name := filepath.Join(ballastDir, fmt.Sprintf("b%06d", i))
			i++
			if err := os.WriteFile(name, chunk, 0o644); err != nil {
				_ = os.Remove(name) // drop the partial file
				break               // ENOSPC at this chunk size; try smaller
			}
			if i > 100000 {
				cleanup()
				t.Fatal("ballast never hit ENOSPC — is MTIX_FAULTFS_DIR really a small volume?")
			}
		}
	}
	return cleanup
}

// assertDBStructurallySound is the incident-signature check from the
// 2026-05-19 RCA: the in-header page count must never exceed the bytes on
// disk (torn checkpoint), and quick_check via the real binary must pass.
func assertDBStructurallySound(t *testing.T, proj string) {
	t.Helper()
	dbPath := filepath.Join(proj, ".mtix", "data", "mtix.db")

	f, err := os.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer f.Close()
	header := make([]byte, 100)
	if _, err := f.ReadAt(header, 0); err != nil {
		t.Fatalf("read header: %v", err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}

	pageSize := uint64(binary.BigEndian.Uint16(header[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	pageCount := uint64(binary.BigEndian.Uint32(header[28:32]))
	if binary.BigEndian.Uint32(header[92:96]) == binary.BigEndian.Uint32(header[24:28]) &&
		pageCount*pageSize > uint64(info.Size()) {
		t.Fatalf("INCIDENT SIGNATURE: header claims %d pages x %d bytes but file is %d bytes",
			pageCount, pageSize, info.Size())
	}

	// The store runs validateDBFile + quick_check on open, so a clean
	// list proves structural integrity end to end.
	if out, err := runMtix(proj, nil, "list"); err != nil {
		t.Fatalf("mtix list after fault: %v\n%s", err, out)
	}
}

// TestDiskFull_PreflightRefusesWrites: with the free-space floor active
// (default), mtix must refuse new writes on a packed volume with an
// actionable error — and the database must stay sound throughout.
func TestDiskFull_PreflightRefusesWrites(t *testing.T) {
	proj := newProject(t, faultFSDir(t))

	if out, err := runMtix(proj, nil, "create", "before disk fills"); err != nil {
		t.Fatalf("create before fill: %v\n%s", err, out)
	}

	cleanup := fillDisk(t, proj)
	defer cleanup()

	out, err := runMtix(proj, nil, "create", "during disk full")
	if err == nil {
		t.Fatalf("create on a full disk must fail loudly, got success:\n%s", out)
	}
	if code := exitCode(err); code != exitDiskFull {
		t.Fatalf("disk-full refusal must exit %d (MTIX-26.8 contract), got %d:\n%s",
			exitDiskFull, code, out)
	}
	if !containsAny(out, "free", "full", "space") {
		t.Fatalf("disk-full refusal must say so; got:\n%s", out)
	}

	cleanup()
	assertDBStructurallySound(t, proj)

	// And the node committed before the incident must still exist.
	listOut, err := runMtix(proj, nil, "list")
	if err != nil {
		t.Fatalf("list after recovery: %v\n%s", err, listOut)
	}
	if !bytes.Contains(listOut, []byte("before disk fills")) {
		t.Fatalf("pre-incident data lost:\n%s", listOut)
	}
}

// TestDiskFull_RealENOSPC: with the pre-flight floor disabled, mtix hits
// genuine ENOSPC inside SQLite. Whatever the write outcome, the database
// must be structurally sound once space is freed — committed-or-absent,
// never torn. This is the exact scenario that destroyed a user's database
// on 2026-05-19.
func TestDiskFull_RealENOSPC(t *testing.T) {
	proj := newProject(t, faultFSDir(t))
	noFloor := []string{"MTIX_MIN_FREE_BYTES=0"}

	if out, err := runMtix(proj, noFloor, "create", "seed node"); err != nil {
		t.Fatalf("seed create: %v\n%s", err, out)
	}

	cleanup := fillDisk(t, proj)
	defer cleanup()

	// Hammer writes into the wall. Failures are expected; silence about
	// them is not.
	for i := 0; i < 10; i++ {
		out, err := runMtix(proj, noFloor, "create", fmt.Sprintf("enospc probe %d", i))
		if err == nil {
			continue // tiny writes can still fit — fine
		}
		if !containsAny(out, "full", "space", "i/o", "disk", "stop") {
			t.Fatalf("ENOSPC failure must be reported as a storage error; got:\n%s", out)
		}
	}

	cleanup()
	assertDBStructurallySound(t, proj)
}

// TestKill9DuringWrites_OnTightDisk: the honest test from the RCA —
// kill -9 mid-write, repeatedly, on a volume close to capacity. After
// every round the database must be openable and structurally sound.
func TestKill9DuringWrites_OnTightDisk(t *testing.T) {
	proj := newProject(t, faultFSDir(t))

	// Moderate ballast: leave the volume tight (but above the pre-flight
	// floor) so checkpoints run close to the edge.
	ballast := bytes.Repeat([]byte{0xCD}, 1<<20)
	for i := 0; i < 4; i++ {
		path := filepath.Join(proj, fmt.Sprintf("ballast-%d", i))
		if err := os.WriteFile(path, ballast, 0o644); err != nil {
			break // smaller volume than expected; proceed with what fits
		}
	}

	for round := 0; round < 15; round++ {
		cmd := exec.Command(mtixBin, "create", fmt.Sprintf("kill round %d", round),
			"--description", string(bytes.Repeat([]byte("x"), 2048)))
		cmd.Dir = proj
		cmd.Env = os.Environ()
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}

		// Vary the kill point across rounds to land in different write
		// phases (init, insert, commit, checkpoint, auto-export).
		time.Sleep(time.Duration(round*7) * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_, _ = cmd.Process.Wait()

		assertDBStructurallySound(t, proj)
	}
}

// TestTruncatedDB_RefusedWithoutTouchingFiles reproduces the 2026-05-19
// end state — header claims more pages than the file holds, no WAL — and
// proves mtix now refuses to open it, names the problem, and leaves every
// byte as it found it (preserving the evidence a recovery needs).
func TestTruncatedDB_RefusedWithoutTouchingFiles(t *testing.T) {
	proj := newProject(t, t.TempDir()) // no small volume needed

	for i := 0; i < 5; i++ {
		if out, err := runMtix(proj, nil, "create", fmt.Sprintf("victim %d", i)); err != nil {
			t.Fatalf("create: %v\n%s", err, out)
		}
	}

	dbPath := filepath.Join(proj, ".mtix", "data", "mtix.db")
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	// Forge the torn-checkpoint header: page count far beyond the file,
	// version-valid-for matching the change counter.
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	header := make([]byte, 100)
	if _, err := f.ReadAt(header, 0); err != nil {
		t.Fatalf("read header: %v", err)
	}
	binary.BigEndian.PutUint32(header[28:32], 1<<20)
	copy(header[92:96], header[24:28])
	if _, err := f.WriteAt(header, 0); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	before := hashFile(t, dbPath)

	out, err := runMtix(proj, nil, "list")
	if err == nil {
		t.Fatalf("opening a truncated DB must fail, got success:\n%s", out)
	}
	if code := exitCode(err); code != exitCorrupted {
		t.Fatalf("corruption refusal must exit %d (MTIX-26.8 contract), got %d:\n%s",
			exitCorrupted, code, out)
	}
	if !containsAny(out, "truncated", "corrupt") {
		t.Fatalf("refusal must name the corruption; got:\n%s", out)
	}

	if hashFile(t, dbPath) != before {
		t.Fatal("mtix modified a damaged database file it refused to open")
	}
	if _, err := os.Stat(dbPath + "-wal"); !os.IsNotExist(err) {
		t.Fatal("mtix created a WAL on a damaged database it refused to open")
	}

	// Recovery path: with the documented escape hatch, salvage commands
	// must get PAST the truncation refusal — a recovery runbook that
	// dead-ends at "cannot open" is no runbook. verify may well report
	// corruption (that is its job); it must not be refused at open.
	hatch := []string{"MTIX_SKIP_INTEGRITY_CHECK=1"}
	out, _ = runMtix(proj, hatch, "verify")
	if containsAny(out, "is truncated") {
		t.Fatalf("escape hatch did not bypass the truncation refusal for verify:\n%s", out)
	}
	out, _ = runMtix(proj, hatch, "export")
	if containsAny(out, "is truncated") {
		t.Fatalf("escape hatch did not bypass the truncation refusal for export:\n%s", out)
	}
}

// TestRecover_TruncatedDB_SalvagesViaMirror is the full incident-recovery
// round trip (MTIX-26.5): the database is torn beyond what SQLite can
// open, mtix recover salvages from the tasks.json mirror, and the result
// imports into a fresh project through the standard validated path.
func TestRecover_TruncatedDB_SalvagesViaMirror(t *testing.T) {
	proj := newProject(t, t.TempDir())

	for i := 0; i < 5; i++ {
		if out, err := runMtix(proj, nil, "create", fmt.Sprintf("salvage victim %d", i)); err != nil {
			t.Fatalf("create: %v\n%s", err, out)
		}
	}

	dbPath := filepath.Join(proj, ".mtix", "data", "mtix.db")
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	header := make([]byte, 100)
	if _, err := f.ReadAt(header, 0); err != nil {
		t.Fatalf("read header: %v", err)
	}
	binary.BigEndian.PutUint32(header[28:32], 1<<20)
	copy(header[92:96], header[24:28])
	if _, err := f.WriteAt(header, 0); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	out, err := runMtix(proj, nil, "recover")
	if err != nil {
		t.Fatalf("mtix recover failed: %v\n%s", err, out)
	}
	matches, err := filepath.Glob(filepath.Join(proj, ".mtix", "recovered-*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one recovered export, got %v (%v)\n%s", matches, err, out)
	}

	fresh := newProject(t, t.TempDir())
	if out, err := runMtix(fresh, nil, "import", "--mode", "replace", matches[0]); err != nil {
		t.Fatalf("import of recovered export failed: %v\n%s", err, out)
	}
	listOut, err := runMtix(fresh, nil, "list")
	if err != nil {
		t.Fatalf("list after recovery import: %v\n%s", err, listOut)
	}
	for i := 0; i < 5; i++ {
		if !bytes.Contains(listOut, []byte(fmt.Sprintf("salvage victim %d", i))) {
			t.Fatalf("node %d missing after recovery round trip:\n%s", i, listOut)
		}
	}
}

// TestDiskFull_MirrorSurvives: the tasks.json written before the volume
// filled must remain intact afterwards — the atomic temp+rename export
// must never destroy the last good mirror.
func TestDiskFull_MirrorSurvives(t *testing.T) {
	proj := newProject(t, faultFSDir(t))

	if out, err := runMtix(proj, nil, "create", "mirrored node"); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	tasksPath := filepath.Join(proj, ".mtix", "tasks.json")
	if _, err := os.Stat(tasksPath); err != nil {
		t.Fatalf("mirror missing after CLI mutation: %v", err)
	}

	cleanup := fillDisk(t, proj)
	defer cleanup()

	// Attempt mutations on the packed volume; exports will fail.
	_, _ = runMtix(proj, nil, "create", "doomed")

	after, err := os.ReadFile(tasksPath)
	if err != nil {
		t.Fatalf("mirror destroyed by failed export: %v", err)
	}
	if !bytes.Contains(after, []byte("mirrored node")) {
		t.Fatalf("mirror no longer contains pre-incident data:\n%.300s", after)
	}
}

// TestDiskFull_SyncBackfill_FailStops drives the DISTRIBUTED-IDENTITY local
// write path under ENOSPC (MTIX-30.11, ADR-003 §7 Phase 0): `mtix sync backfill`
// synthesizes uid-keyed create events into the local sync_events queue (the
// Phase 0 UID-backfill the migration depends on). On a packed volume that write
// must FAIL-STOP per NFR-2.8 — a loud storage error, never a silently corrupt
// or half-written queue — and the pre-existing data and mirror must survive.
func TestDiskFull_SyncBackfill_FailStops(t *testing.T) {
	proj := newProject(t, faultFSDir(t))

	// Seed nodes (and thus sync_events) on a healthy disk.
	for i := 0; i < 5; i++ {
		if out, err := runMtix(proj, nil, "create", fmt.Sprintf("identity node %d", i)); err != nil {
			t.Fatalf("seed create %d: %v\n%s", i, err, out)
		}
	}

	cleanup := fillDisk(t, proj)
	defer cleanup()

	// --force re-synthesizes the uid-keyed events even though sync_events is
	// non-empty — the exact write the Phase 0 migration performs. On a full
	// volume it must fail loudly, not corrupt the store.
	out, err := runMtix(proj, nil, "sync", "backfill", "--force")
	if err == nil {
		t.Fatalf("sync backfill on a full disk must fail-stop, got success:\n%s", out)
	}
	// Disk-full on a sync write must honor the NFR-2.8 exit-code contract
	// (MTIX-32): exit disk-full, same as `mtix create`. (Previously this
	// accepted exit 1 + a storage message because wrapSyncErr stringified the
	// error and broke the errors.Is chain; that is fixed.)
	if code := exitCode(err); code != exitDiskFull {
		t.Fatalf("backfill ENOSPC must exit %d (NFR-2.8/MTIX-32); got exit %d:\n%s",
			exitDiskFull, code, out)
	}

	cleanup()
	assertDBStructurallySound(t, proj)

	// The pre-incident nodes must still be intact — the failed backfill must
	// not have torn the canonical store.
	listOut, err := runMtix(proj, nil, "list")
	if err != nil {
		t.Fatalf("list after backfill fail-stop: %v\n%s", err, listOut)
	}
	for i := 0; i < 5; i++ {
		if !bytes.Contains(listOut, []byte(fmt.Sprintf("identity node %d", i))) {
			t.Fatalf("identity node %d lost after a failed backfill:\n%s", i, listOut)
		}
	}
}

// TestKill9DuringSyncBackfill_OnTightDisk is the literal kill -9 mid-operation
// crash test for a distributed-identity write (MTIX-30.11, ADR-003 §7 Phase 0):
// SIGKILL `mtix sync backfill` repeatedly, at varying phases, on a volume close
// to capacity. The CLI documents single-tx (SIGKILL-safe) atomicity for the
// event synthesis; after every kill the database must stay openable and
// structurally sound, and a final clean backfill must RESUME and converge.
func TestKill9DuringSyncBackfill_OnTightDisk(t *testing.T) {
	proj := newProject(t, faultFSDir(t))
	for i := 0; i < 5; i++ {
		if out, err := runMtix(proj, nil, "create", fmt.Sprintf("kill backfill seed %d", i)); err != nil {
			t.Fatalf("seed create %d: %v\n%s", i, err, out)
		}
	}

	// Leave the volume tight (but with headroom) so checkpoints run near the
	// edge while the kill lands.
	ballast := bytes.Repeat([]byte{0xCD}, 1<<20)
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(proj, fmt.Sprintf("ballast-%d", i)), ballast, 0o644); err != nil {
			break
		}
	}

	for round := 0; round < 12; round++ {
		cmd := exec.Command(mtixBin, "sync", "backfill", "--force")
		cmd.Dir = proj
		cmd.Env = os.Environ()
		if err := cmd.Start(); err != nil {
			t.Fatalf("start backfill round %d: %v", round, err)
		}
		// Vary the kill point across rounds to land in different phases
		// (pushlock acquire, event walk, insert, commit).
		time.Sleep(time.Duration(round*5) * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_, _ = cmd.Process.Wait()

		// flock is released by the kernel on process death, so the next
		// backfill is never wedged by a stale lock.
		assertDBStructurallySound(t, proj)
	}

	// Resume converges: a clean backfill now succeeds and every seed node is
	// still present (no loss across the kill storm).
	if out, err := runMtix(proj, nil, "sync", "backfill", "--force"); err != nil {
		t.Fatalf("backfill must resume cleanly after the kill storm: %v\n%s", err, out)
	}
	listOut, err := runMtix(proj, nil, "list")
	if err != nil {
		t.Fatalf("list after resume: %v\n%s", err, listOut)
	}
	for i := 0; i < 5; i++ {
		if !bytes.Contains(listOut, []byte(fmt.Sprintf("kill backfill seed %d", i))) {
			t.Fatalf("seed node %d lost across the kill storm:\n%s", i, listOut)
		}
	}
}

func hashFile(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sha256.Sum256(data)
}
