// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package keyring holds the MAC keys of an FR-21 file relay, one per
// key epoch, and selects among them by the epoch a frame declares.
//
// Keys live in a directory rather than a file — one file per epoch,
// named in plain decimal — because a rotation is not a swap. FR-21
// D-R10 requires a reader mid-rotation to distinguish "old key, valid,
// pre-boundary" from "wrong key, forged", and it can only do that while
// the pre-boundary key is still loadable. A single key file would make
// every record published before the boundary indistinguishable from a
// forgery the moment the rotation landed.
//
// The selection rule follows from the same requirement: the key is
// chosen by the epoch the frame carries, never by what happens to be
// newest, and an epoch with no key file is a loud verdict rather than a
// fallback. Falling back would report a missing key as a forged record
// and send an operator hunting an attacker who is not there.
//
// The package is pure: standard library only, no store imports, no
// goroutines. It does not mint key material or read configuration — a
// caller supplies the directory and the randomness source.
package keyring

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// KeyFileMode is the exact permission an epoch key file must carry.
//
// The check is equality, not a mask, so 0640 and 0400 both refuse: this
// mirrors the shipped secrets-file discipline, where anything other
// than owner-only read/write is treated as a key to rotate rather than
// a key to use.
//
// The shipped check lives in the store's transport package, which this
// layer may not import — the relay layer carries no store dependency.
// The duplication is deliberate and tracked as MTIX-69: refactoring a
// shipped, fail-closed security path from inside a feature ticket is
// how a five-line check acquires an unreviewed behavior change, so the
// extraction runs as its own reviewed piece of work.
const KeyFileMode os.FileMode = 0o600

// KeyDirMode is the exact permission the keys directory must carry. A
// world-readable directory lets another account on the box enumerate
// and replace epoch files even when each file is 0600.
const KeyDirMode os.FileMode = 0o700

// MinKeyBytes is the minimum key length accepted, matching the
// HMAC-SHA256 block output the relay authenticates with. Shorter
// material is refused rather than stretched.
const MinKeyBytes = 32

// Verdict codes surfaced to operators through status and doctor.
const (
	// CodeKeyAbsent marks an epoch with no key file, or a relay with no
	// keys at all. The recovery is to install the key, never to guess.
	CodeKeyAbsent = "RELAY_KEY_ABSENT"

	// CodeKeyPerms marks key material readable beyond its owner.
	CodeKeyPerms = "RELAY_KEY_PERMS"

	// CodeKeyInvalid marks material that is not a usable key.
	CodeKeyInvalid = "RELAY_KEY_INVALID"

	// CodeKeySymlink marks a symlink in the key path (CWE-59).
	CodeKeySymlink = "RELAY_KEY_SYMLINK"
)

// Verdict sentinels. Callers dispatch with errors.Is and surface
// CodeOf(err) to operators.
var (
	// ErrKeyAbsent is RELAY_KEY_ABSENT.
	ErrKeyAbsent = errors.New(CodeKeyAbsent + ": no key for the requested epoch")

	// ErrKeyPerms is RELAY_KEY_PERMS.
	ErrKeyPerms = errors.New(CodeKeyPerms + ": key material is readable beyond its owner")

	// ErrKeyInvalid is RELAY_KEY_INVALID.
	ErrKeyInvalid = errors.New(CodeKeyInvalid + ": key material is unusable")

	// ErrKeySymlink is RELAY_KEY_SYMLINK.
	ErrKeySymlink = errors.New(CodeKeySymlink + ": symlink in the key path")

	// ErrKeyExists refuses to replace an epoch that already has a key.
	ErrKeyExists = errors.New("key epoch already has a key")
)

// epochNamePattern is the epoch file name: plain decimal, no padding,
// no sign. Leading zeros are refused rather than normalized — two names
// for one epoch is one name too many in a directory that decides what
// the fleet authenticates with.
var epochNamePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// CodeOf returns the RELAY_* code a verdict carries, or "" when err is
// nil or is not one of this package's verdicts.
func CodeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrKeySymlink):
		return CodeKeySymlink
	case errors.Is(err, ErrKeyPerms):
		return CodeKeyPerms
	case errors.Is(err, ErrKeyInvalid):
		return CodeKeyInvalid
	case errors.Is(err, ErrKeyAbsent):
		return CodeKeyAbsent
	default:
		return ""
	}
}

// Ring is the set of keys a peer can authenticate with, by epoch.
type Ring struct {
	keys    map[uint16][]byte
	epochs  []uint16
	foreign []string
}

// Epochs returns the epochs present, ascending.
func (r *Ring) Epochs() []uint16 { return r.epochs }

// Foreign returns the names in the keys directory that are not epoch
// files. They are reported for doctor and never read — a keys directory
// accumulates editor leftovers like any other.
func (r *Ring) Foreign() []string { return r.foreign }

// For returns the key for an epoch.
//
// A missing epoch is ErrKeyAbsent and never another epoch's key. This
// is the D-R10 boundary: with the right key the frame verifies, with
// the wrong key it reads as forged, and with no key at all the operator
// is told exactly that.
func (r *Ring) For(epoch uint16) ([]byte, error) {
	key, ok := r.keys[epoch]
	if !ok {
		return nil, fmt.Errorf("%w: epoch %d (present: %s)", ErrKeyAbsent, epoch, formatEpochs(r.epochs))
	}
	return key, nil
}

// Current returns the highest epoch present and its key — what a
// publisher writes under.
func (r *Ring) Current() (uint16, []byte, error) {
	if len(r.epochs) == 0 {
		return 0, nil, fmt.Errorf("%w: the ring is empty", ErrKeyAbsent)
	}
	epoch := r.epochs[len(r.epochs)-1]
	return epoch, r.keys[epoch], nil
}

// formatEpochs renders the epochs present for a verdict message, so an
// operator sees what is installed alongside what was asked for.
func formatEpochs(epochs []uint16) string {
	if len(epochs) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(epochs))
	for _, e := range epochs {
		parts = append(parts, strconv.FormatUint(uint64(e), 10))
	}
	return strings.Join(parts, ",")
}

// Load reads every epoch key in dir.
//
// A missing or empty directory is ErrKeyAbsent rather than an I/O
// error, so a caller can tell "no keys configured" — the
// unauthenticated-mode and fresh-install path — from a broken disk.
func Load(dir string) (*Ring, error) {
	if err := checkDir(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read key directory %s: %w", dir, err)
	}

	ring := &Ring{keys: make(map[uint16][]byte)}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s", ErrKeySymlink, filepath.Join(dir, e.Name()))
		}
		epoch, ok := parseEpochName(e.Name())
		if !ok || !e.Type().IsRegular() {
			ring.foreign = append(ring.foreign, e.Name())
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", filepath.Join(dir, e.Name()), err)
		}
		key, err := readKeyFile(filepath.Join(dir, e.Name()), info.Mode().Perm())
		if err != nil {
			return nil, err
		}
		ring.keys[epoch] = key
		ring.epochs = append(ring.epochs, epoch)
	}
	if len(ring.epochs) == 0 {
		return nil, fmt.Errorf("%w: no epoch keys in %s", ErrKeyAbsent, dir)
	}
	sort.Slice(ring.epochs, func(i, j int) bool { return ring.epochs[i] < ring.epochs[j] })
	return ring, nil
}

// checkDir verifies the keys directory exists, is a real directory, and
// is owner-only.
func checkDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: no key directory at %s", ErrKeyAbsent, dir)
		}
		return fmt.Errorf("lstat %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrKeySymlink, dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrKeyInvalid, dir)
	}
	if mode := info.Mode().Perm(); mode != KeyDirMode {
		return fmt.Errorf("%w: %s is %#o (want %#o)", ErrKeyPerms, dir, mode, KeyDirMode)
	}
	return nil
}

// parseEpochName reads an epoch file name.
func parseEpochName(name string) (uint16, bool) {
	if !epochNamePattern.MatchString(name) {
		return 0, false
	}
	n, err := strconv.ParseUint(name, 10, 16)
	if err != nil {
		return 0, false
	}
	return uint16(n), true
}

// readKeyFile reads one epoch key, enforcing the mode discipline. The
// mode comes from the directory read that found the file, so there is
// no second stat to disagree with the first.
//
// No error from this function includes file contents: a message that
// echoes malformed key material into a log is a key disclosure with
// extra steps.
func readKeyFile(path string, mode os.FileMode) ([]byte, error) {
	if mode != KeyFileMode {
		return nil, fmt.Errorf("%w: %s is %#o (want %#o)", ErrKeyPerms, path, mode, KeyFileMode)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- path is an epoch file in the caller's verified key directory
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	key, err := decodeKey(strings.TrimSpace(string(body)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return key, nil
}

// decodeKey decodes and length-checks base64 key material.
func decodeKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, fmt.Errorf("%w: file is empty", ErrKeyInvalid)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: not base64", ErrKeyInvalid)
	}
	if len(key) < MinKeyBytes {
		return nil, fmt.Errorf("%w: %d bytes, minimum is %d", ErrKeyInvalid, len(key), MinKeyBytes)
	}
	return key, nil
}

// Write installs the key for an epoch, creating the keys directory with
// the required mode if it is absent.
//
// It never replaces an existing epoch. Replacing one would invalidate
// every record already published under it, retroactively turning the
// fleet's own history into forgeries — so a re-run of a rotation
// refuses rather than destroys.
func Write(dir string, epoch uint16, key []byte) error {
	if len(key) < MinKeyBytes {
		return fmt.Errorf("%w: %d bytes, minimum is %d", ErrKeyInvalid, len(key), MinKeyBytes)
	}
	// MkdirAll honors the process umask, and an existing directory keeps
	// whatever mode it had, so the mode is asserted rather than assumed.
	if err := errors.Join(os.MkdirAll(dir, KeyDirMode), os.Chmod(dir, KeyDirMode)); err != nil {
		return fmt.Errorf("prepare key directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, strconv.FormatUint(uint64(epoch), 10))
	body := base64.StdEncoding.EncodeToString(key) + "\n"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, KeyFileMode) // #nosec G304 -- name is the decimal epoch, not caller text
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%w: epoch %d", ErrKeyExists, epoch)
		}
		return fmt.Errorf("create key %s: %w", path, err)
	}
	// O_CREATE honors the umask too, so the mode is asserted rather than
	// assumed — this file is the fleet's authentication material, and a
	// mode the umask narrowed to 0400 is one this package's own loader
	// would refuse.
	_, writeErr := f.WriteString(body)
	if err := errors.Join(writeErr, f.Close(), os.Chmod(path, KeyFileMode)); err != nil {
		// Leave no half-written key behind: it would fail to load as
		// RELAY_KEY_INVALID and, worse, would make a retry of the
		// rotation refuse the epoch as already taken.
		_ = os.Remove(path)
		return fmt.Errorf("write key %s: %w", path, err)
	}
	return nil
}

// Generate draws MinKeyBytes of key material from rand, which the
// caller supplies (crypto/rand.Reader in production).
//
// A short read is a refusal, never a shorter key: silently accepting
// partial entropy is how a fleet ends up authenticating with something
// guessable.
func Generate(rand io.Reader) ([]byte, error) {
	key := make([]byte, MinKeyBytes)
	if _, err := io.ReadFull(rand, key); err != nil {
		return nil, fmt.Errorf("generate relay key: %w", err)
	}
	return key, nil
}
