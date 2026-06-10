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
	if !containsAny(out, "truncated", "corrupt") {
		t.Fatalf("refusal must name the corruption; got:\n%s", out)
	}

	if hashFile(t, dbPath) != before {
		t.Fatal("mtix modified a damaged database file it refused to open")
	}
	if _, err := os.Stat(dbPath + "-wal"); !os.IsNotExist(err) {
		t.Fatal("mtix created a WAL on a damaged database it refused to open")
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

func hashFile(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sha256.Sum256(data)
}
