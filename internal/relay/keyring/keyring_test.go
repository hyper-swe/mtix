// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package keyring_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/stretchr/testify/require"
)

// writeKey installs an epoch key file the way the operator commands do.
func writeKey(t *testing.T, dir string, name string, key []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	body := base64.StdEncoding.EncodeToString(key) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}

func testKeyN(n byte) []byte {
	k := make([]byte, keyring.MinKeyBytes)
	for i := range k {
		k[i] = n
	}
	return k
}

// TestLoad_SelectsByEpoch is the reader-side selection rule: the key is
// chosen by the epoch the frame declares, never by what happens to be
// newest. A rotation is only provable from the frame if the frame picks
// the key.
func TestLoad_SelectsByEpoch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	writeKey(t, dir, "1", testKeyN(0xaa))
	writeKey(t, dir, "2", testKeyN(0xbb))

	ring, err := keyring.Load(dir)
	require.NoError(t, err)
	require.Equal(t, []uint16{1, 2}, ring.Epochs())

	k1, err := ring.For(1)
	require.NoError(t, err)
	require.Equal(t, testKeyN(0xaa), k1)

	k2, err := ring.For(2)
	require.NoError(t, err)
	require.Equal(t, testKeyN(0xbb), k2)
}

// TestRing_CurrentIsTheHighestEpochPresent is the publisher-side rule.
func TestRing_CurrentIsTheHighestEpochPresent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	writeKey(t, dir, "3", testKeyN(0xcc))
	writeKey(t, dir, "1", testKeyN(0xaa))
	writeKey(t, dir, "2", testKeyN(0xbb))

	ring, err := keyring.Load(dir)
	require.NoError(t, err)

	epoch, key, err := ring.Current()
	require.NoError(t, err)
	require.Equal(t, uint16(3), epoch)
	require.Equal(t, testKeyN(0xcc), key)
}

// TestRing_UnknownEpochIsLoud is the D-R10 rule that keeps a rotation
// honest. An epoch with no key file is RELAY_KEY_ABSENT — an operator
// problem with a named recovery — and never a silent fallback to
// another epoch's key, which would turn a missing key into a forgery
// verdict and send the operator hunting the wrong thing.
func TestRing_UnknownEpochIsLoud(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	writeKey(t, dir, "2", testKeyN(0xbb))

	ring, err := keyring.Load(dir)
	require.NoError(t, err)

	for _, epoch := range []uint16{0, 1, 3, 65535} {
		_, err := ring.For(epoch)
		require.ErrorIs(t, err, keyring.ErrKeyAbsent, "epoch %d", epoch)
		require.Equal(t, "RELAY_KEY_ABSENT", keyring.CodeOf(err))
		require.Contains(t, err.Error(), "2", "the verdict should name the epochs that are present")
	}
}

// TestLoad_RefusesPermissiveModes is the fail-closed discipline of the
// shipped secrets path, applied to key material: the check is exact
// equality, so 0640 and 0400 both refuse. A key readable by another
// account on the box is a key to treat as compromised, and a
// silently-accepted one is worse than a refused one.
func TestLoad_RefusesPermissiveModes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	tests := []struct {
		name string
		mode os.FileMode
	}{
		{"group readable", 0o640},
		{"world readable", 0o644},
		{"group writable", 0o660},
		{"executable", 0o700},
		{"read only", 0o400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "keys")
			writeKey(t, dir, "1", testKeyN(0xaa))
			require.NoError(t, os.Chmod(filepath.Join(dir, "1"), tt.mode))

			_, err := keyring.Load(dir)
			require.ErrorIs(t, err, keyring.ErrKeyPerms)
			require.Equal(t, "RELAY_KEY_PERMS", keyring.CodeOf(err))
			require.Contains(t, err.Error(), "0600", "the verdict must name the required mode")
		})
	}
}

// TestLoad_RefusesAPermissiveDirectory closes the same hole one level
// up: a 0755 key directory lets another account enumerate and replace
// epoch files even when each file is 0600.
func TestLoad_RefusesAPermissiveDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir := filepath.Join(t.TempDir(), "keys")
	writeKey(t, dir, "1", testKeyN(0xaa))
	require.NoError(t, os.Chmod(dir, 0o755))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := keyring.Load(dir)
	require.ErrorIs(t, err, keyring.ErrKeyPerms)
	require.Contains(t, err.Error(), "0700")
}

// TestLoad_MissingDirectoryIsAbsentNotAnIOError lets a caller tell "no
// keys configured" from "the disk is broken" — the first is the
// unauthenticated-mode path and a fresh-install path, the second is not.
func TestLoad_MissingDirectoryIsAbsentNotAnIOError(t *testing.T) {
	_, err := keyring.Load(filepath.Join(t.TempDir(), "keys"))
	require.ErrorIs(t, err, keyring.ErrKeyAbsent)
}

// TestLoad_EmptyDirectoryIsAbsent covers a keys directory created but
// never populated.
func TestLoad_EmptyDirectoryIsAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	_, err := keyring.Load(dir)
	require.ErrorIs(t, err, keyring.ErrKeyAbsent)
}

// TestLoad_EpochFileNames pins the naming rule: plain decimal, no
// padding, within the frame's u16. Anything else is reported as a
// foreign entry rather than guessed at — an epoch read wrong would
// authenticate frames against the wrong key.
func TestLoad_EpochFileNames(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		foreign bool
	}{
		{"zero", "0", false},
		{"one", "1", false},
		{"max u16", "65535", false},
		{"past u16", "65536", true},
		{"leading zero", "01", true},
		{"negative", "-1", true},
		{"hex", "0x1", true},
		{"editor swap file", "1.swp", true},
		{"backup", "1~", true},
		{"readme", "README", true},
		{"empty-ish", ".keep", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "keys")
			// A known-good epoch so the ring is never empty.
			writeKey(t, dir, "7", testKeyN(0x77))
			writeKey(t, dir, tt.file, testKeyN(0xaa))

			ring, err := keyring.Load(dir)
			require.NoError(t, err)
			if tt.foreign {
				require.Equal(t, []uint16{7}, ring.Epochs())
				require.Contains(t, ring.Foreign(), tt.file)
				return
			}
			require.Contains(t, ring.Epochs(), uint16(7))
			require.Len(t, ring.Epochs(), 2)
			require.Empty(t, ring.Foreign())
		})
	}
}

// TestLoad_RefusesUnusableKeyMaterial keeps a malformed key from
// becoming a fleet-wide authentication failure that looks like an
// attack.
func TestLoad_RefusesUnusableKeyMaterial(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty file", ""},
		{"whitespace only", "   \n\t\n"},
		{"not base64", "!!!! not base64 !!!!"},
		{"base64 of nothing", ""},
		{"too short", base64.StdEncoding.EncodeToString([]byte("short"))},
		{"one byte under the minimum", base64.StdEncoding.EncodeToString(make([]byte, keyring.MinKeyBytes-1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "keys")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "1"), []byte(tt.body), 0o600))

			_, err := keyring.Load(dir)
			require.ErrorIs(t, err, keyring.ErrKeyInvalid)
			require.Equal(t, "RELAY_KEY_INVALID", keyring.CodeOf(err))
		})
	}
}

// TestLoad_ToleratesSurroundingWhitespace matches the shipped secrets
// path, which trims the whole file — operators edit these by hand.
func TestLoad_ToleratesSurroundingWhitespace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	body := "\n  " + base64.StdEncoding.EncodeToString(testKeyN(0xaa)) + "  \n\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1"), []byte(body), 0o600))

	ring, err := keyring.Load(dir)
	require.NoError(t, err)
	k, err := ring.For(1)
	require.NoError(t, err)
	require.Equal(t, testKeyN(0xaa), k)
}

// TestLoad_RefusesSymlinks applies the CWE-59 discipline to key
// material. The keys directory is on the local filesystem rather than
// the shared medium, but a link here redirects what the fleet
// authenticates with, which is worth more than a segment.
func TestLoad_RefusesSymlinks(t *testing.T) {
	t.Run("an epoch file that is a link", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "keys")
		writeKey(t, dir, "1", testKeyN(0xaa))
		elsewhere := filepath.Join(root, "elsewhere")
		require.NoError(t, os.WriteFile(elsewhere, []byte("x"), 0o600))
		require.NoError(t, os.Symlink(elsewhere, filepath.Join(dir, "2")))

		_, err := keyring.Load(dir)
		require.ErrorIs(t, err, keyring.ErrKeySymlink)
		require.Equal(t, "RELAY_KEY_SYMLINK", keyring.CodeOf(err))
	})

	t.Run("a keys directory that is a link", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		writeKey(t, target, "1", testKeyN(0xaa))
		link := filepath.Join(root, "keys")
		require.NoError(t, os.Symlink(target, link))

		_, err := keyring.Load(link)
		require.ErrorIs(t, err, keyring.ErrKeySymlink)
	})
}

// TestWrite_InstallsAnEpochKey covers the operator-command side.
func TestWrite_InstallsAnEpochKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, keyring.Write(dir, 1, testKeyN(0xaa)))

	info, err := os.Stat(filepath.Join(dir, "1"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "key files are 0600")

	dinfo, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dinfo.Mode().Perm(), "the keys directory is 0700")

	ring, err := keyring.Load(dir)
	require.NoError(t, err)
	k, err := ring.For(1)
	require.NoError(t, err)
	require.Equal(t, testKeyN(0xaa), k)
}

// TestWrite_NeverOverwritesAnEpoch is what makes a rotation safe to
// re-run. Replacing the key for an epoch would invalidate every record
// already published under it, retroactively turning the fleet's own
// history into forgeries.
func TestWrite_NeverOverwritesAnEpoch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, keyring.Write(dir, 1, testKeyN(0xaa)))

	err := keyring.Write(dir, 1, testKeyN(0xbb))
	require.ErrorIs(t, err, keyring.ErrKeyExists)

	// The original is intact.
	ring, err := keyring.Load(dir)
	require.NoError(t, err)
	k, err := ring.For(1)
	require.NoError(t, err)
	require.Equal(t, testKeyN(0xaa), k)
}

// TestWrite_RefusesWeakKeyMaterial keeps a short key out of the fleet.
func TestWrite_RefusesWeakKeyMaterial(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	require.ErrorIs(t, keyring.Write(dir, 1, nil), keyring.ErrKeyInvalid)
	require.ErrorIs(t, keyring.Write(dir, 1, make([]byte, keyring.MinKeyBytes-1)), keyring.ErrKeyInvalid)
}

// TestGenerate draws key material from the caller's randomness source,
// so the package stays free of hidden global state and a test can prove
// a CSPRNG failure is surfaced rather than swallowed.
func TestGenerate(t *testing.T) {
	t.Run("produces a key of the required strength", func(t *testing.T) {
		key, err := keyring.Generate(strings.NewReader(strings.Repeat("s", 128)))
		require.NoError(t, err)
		require.Len(t, key, keyring.MinKeyBytes)
	})
	t.Run("a short randomness source is a refusal, never a weak key", func(t *testing.T) {
		_, err := keyring.Generate(strings.NewReader("not enough entropy"))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "not enough entropy", "never echo key material")
	})
}

// TestLoad_RefusesAFileWhereTheDirectoryBelongs covers a keys path that
// is not a directory at all.
func TestLoad_RefusesAFileWhereTheDirectoryBelongs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	_, err := keyring.Load(path)
	require.ErrorIs(t, err, keyring.ErrKeyInvalid)
}

// TestLoad_UnreadableDirectoryReportsTheIOError keeps a permission
// problem on the enclosing path distinct from a verdict about keys.
func TestLoad_UnreadableDirectoryReportsTheIOError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "keys")
	writeKey(t, dir, "1", testKeyN(0xaa))
	require.NoError(t, os.Chmod(root, 0o000))
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	_, err := keyring.Load(dir)
	require.Error(t, err)
	require.Equal(t, "", keyring.CodeOf(err), "an I/O failure is not a key verdict")
}

// TestWrite_UnwritableParentIsReported covers the install path failing
// on the medium rather than on policy.
func TestWrite_UnwritableParentIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o500))
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	err := keyring.Write(filepath.Join(root, "keys"), 1, testKeyN(0xaa))
	require.Error(t, err)
	require.Equal(t, "", keyring.CodeOf(err))
}

// TestWrite_CorrectsAPermissiveDirectory asserts the mode is set rather
// than assumed: MkdirAll honors the process umask, and an existing
// directory keeps whatever mode it had.
func TestWrite_CorrectsAPermissiveDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	require.NoError(t, keyring.Write(dir, 1, testKeyN(0xaa)))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	// And the ring loads, which it would not through a 0755 directory.
	_, err = keyring.Load(dir)
	require.NoError(t, err)
}

// TestWrite_RefusesWhenTheEpochNameIsTaken covers an epoch path that
// exists but is not a key file — a stale directory from a half-finished
// operation, say. Exclusive creation refuses it as an occupied epoch,
// which is the same operational answer: that slot is taken, and nothing
// here will clobber it to find out by what.
func TestWrite_RefusesWhenTheEpochNameIsTaken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	occupied := filepath.Join(dir, "1")
	require.NoError(t, os.MkdirAll(occupied, 0o700))

	require.Error(t, keyring.Write(dir, 1, testKeyN(0xaa)))

	// Whatever was there is still there.
	info, err := os.Lstat(occupied)
	require.NoError(t, err)
	require.True(t, info.IsDir())
}
