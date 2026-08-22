// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/stretchr/testify/require"
)

const (
	peerA = "0123456789abcdef"
	peerB = "fedcba9876543210"
)

var fixedTime = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func mtixProject() metadata.Project {
	return metadata.Project{Prefix: "MTIX", FirstEventHash: "aaaa1111"}
}

// initReq is an authenticated relay carrying one project.
func initReq(relayDir, keysDir string) lifecycle.InitRequest {
	return lifecycle.InitRequest{
		RelayDir:      relayDir,
		KeysDir:       keysDir,
		RelayID:       "01a0238b-d7d5-77cc-95c6-98a472ed7803",
		CreatedAt:     fixedTime,
		CreatedBy:     peerA,
		Projects:      []metadata.Project{mtixProject()},
		Authenticated: true,
		Rand:          rand.Reader,
	}
}

func dirs(t *testing.T) (relayDir, keysDir string) {
	t.Helper()
	root := t.TempDir()
	relayDir = filepath.Join(root, "relay")
	require.NoError(t, os.MkdirAll(relayDir, 0o700))
	return relayDir, filepath.Join(root, "keys")
}

// TestInit_CreatesTheRecordAndTheFirstKey covers `relay init` on an
// authenticated relay: the record and epoch 1's key land together, so a
// relay is never published to before it can be authenticated.
func TestInit_CreatesTheRecordAndTheFirstKey(t *testing.T) {
	relayDir, keysDir := dirs(t)

	res, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)
	require.Equal(t, metadata.FirstKeyEpoch, res.KeyEpoch)
	require.True(t, res.Relay.Authenticated)

	doc, err := metadata.Read(relayDir)
	require.NoError(t, err)
	require.Len(t, doc.KeyEpochs, 1)

	ring, err := keyring.Load(keysDir)
	require.NoError(t, err)
	epoch, key, err := ring.Current()
	require.NoError(t, err)
	require.Equal(t, metadata.FirstKeyEpoch, epoch)
	require.Len(t, key, keyring.MinKeyBytes)
}

// TestInit_Unauthenticated is the §8.3 opt-out: explicit at init,
// recorded in the record, and it mints no key at all.
func TestInit_Unauthenticated(t *testing.T) {
	relayDir, keysDir := dirs(t)
	req := initReq(relayDir, keysDir)
	req.Authenticated = false

	res, err := lifecycle.Init(req)
	require.NoError(t, err)
	require.False(t, res.Relay.Authenticated)
	require.Zero(t, res.KeyEpoch)

	_, err = keyring.Load(keysDir)
	require.ErrorIs(t, err, keyring.ErrKeyAbsent, "an unauthenticated relay mints no key")
}

// TestInit_RefusesAnExistingRelay stops an init over a live relay,
// which would orphan every peer already attached to it.
func TestInit_RefusesAnExistingRelay(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	_, err = lifecycle.Init(initReq(relayDir, keysDir))
	require.ErrorIs(t, err, metadata.ErrRelayExists)
}

// TestAttach_Succeeds covers the §6.5 handshake passing.
func TestAttach_Succeeds(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	res, err := lifecycle.Attach(lifecycle.AttachRequest{
		RelayDir:      relayDir,
		KeysDir:       keysDir,
		Local:         []metadata.Project{mtixProject(), {Prefix: "OTHER", FirstEventHash: "zzzz"}},
		Authenticated: true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"MTIX"}, res.SharedProjects)
	require.Equal(t, metadata.FirstKeyEpoch, res.KeyEpoch)
}

// TestAttach_FailsClosedWithoutTheKey is the §6.5 rule that an
// authenticated relay does not complete an attach until the key is
// present. Attaching first and discovering the key later would leave a
// peer configured for a relay it cannot read, publishing nothing and
// reporting no reason.
func TestAttach_FailsClosedWithoutTheKey(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	// The joining peer has the record but not the key material.
	_, err = lifecycle.Attach(lifecycle.AttachRequest{
		RelayDir:      relayDir,
		KeysDir:       filepath.Join(t.TempDir(), "keys"),
		Local:         []metadata.Project{mtixProject()},
		Authenticated: true,
	})
	require.ErrorIs(t, err, keyring.ErrKeyAbsent)
	require.Equal(t, "RELAY_KEY_ABSENT", keyring.CodeOf(err))
}

// TestAttach_RequiresTheKeyForTheCurrentEpoch closes the narrower gap:
// a peer holding only a retired epoch's key can read history but cannot
// authenticate anything current.
func TestAttach_RequiresTheKeyForTheCurrentEpoch(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)
	_, err = lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		FromRSByPeer: map[string]uint64{peerA: 51}, Rand: rand.Reader,
	})
	require.NoError(t, err)

	// A peer that only ever received epoch 1.
	stale := filepath.Join(t.TempDir(), "keys")
	ring, err := keyring.Load(keysDir)
	require.NoError(t, err)
	old, err := ring.For(1)
	require.NoError(t, err)
	require.NoError(t, keyring.Write(stale, 1, old))

	_, err = lifecycle.Attach(lifecycle.AttachRequest{
		RelayDir: relayDir, KeysDir: stale,
		Local:         []metadata.Project{mtixProject()},
		Authenticated: true,
	})
	require.ErrorIs(t, err, keyring.ErrKeyAbsent)
}

// TestAttach_RefusesMixedModes is §8.3: authenticated and
// unauthenticated peers on one relay are a refusal, in both directions.
// Inferring the mode from the file would let a relay silently downgrade
// a peer that was configured to require authentication.
func TestAttach_RefusesMixedModes(t *testing.T) {
	tests := []struct {
		name          string
		relayAuth     bool
		peerAuth      bool
		expectRefusal bool
	}{
		{"both authenticated", true, true, false},
		{"both unauthenticated", false, false, false},
		{"authenticated peer, unauthenticated relay", false, true, true},
		{"unauthenticated peer, authenticated relay", true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relayDir, keysDir := dirs(t)
			req := initReq(relayDir, keysDir)
			req.Authenticated = tt.relayAuth
			_, err := lifecycle.Init(req)
			require.NoError(t, err)

			_, err = lifecycle.Attach(lifecycle.AttachRequest{
				RelayDir: relayDir, KeysDir: keysDir,
				Local:         []metadata.Project{mtixProject()},
				Authenticated: tt.peerAuth,
			})
			if tt.expectRefusal {
				require.ErrorIs(t, err, lifecycle.ErrModeMismatch)
				require.Equal(t, "RELAY_MODE_MISMATCH", lifecycle.CodeOf(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestAttach_RefusesADivergentHistory is the §6.5 reuse of the
// divergent-history check: two stores whose first events differ are
// different histories wearing one prefix, and merging them would
// interleave two unrelated projects under one set of ids.
func TestAttach_RefusesADivergentHistory(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	_, err = lifecycle.Attach(lifecycle.AttachRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		Local:         []metadata.Project{{Prefix: "MTIX", FirstEventHash: "different"}},
		Authenticated: true,
	})
	require.ErrorIs(t, err, lifecycle.ErrHistoryDiverged)
	require.Equal(t, "RELAY_HISTORY_DIVERGED", lifecycle.CodeOf(err))
	require.Contains(t, err.Error(), "MTIX")
}

// TestAttach_RefusesWithNoSharedProject stops a peer joining a relay it
// has nothing in common with, where it would publish into a stream no
// one can apply.
func TestAttach_RefusesWithNoSharedProject(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	_, err = lifecycle.Attach(lifecycle.AttachRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		Local:         []metadata.Project{{Prefix: "OTHER", FirstEventHash: "zzzz"}},
		Authenticated: true,
	})
	require.ErrorIs(t, err, lifecycle.ErrNoSharedProject)
	require.Equal(t, "RELAY_NO_SHARED_PROJECT", lifecycle.CodeOf(err))
}

// TestAttach_RefusesAnUnreadableRelay covers §5.6's statement that
// corruption blocks attach.
func TestAttach_RefusesAnUnreadableRelay(t *testing.T) {
	relayDir, keysDir := dirs(t)
	require.NoError(t, os.WriteFile(filepath.Join(relayDir, metadata.FileName), []byte("{ broken"), 0o600))

	_, err := lifecycle.Attach(lifecycle.AttachRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		Local:         []metadata.Project{mtixProject()},
		Authenticated: true,
	})
	require.ErrorIs(t, err, metadata.ErrRelayCorrupt)
}

// TestRotateKey_InstallsTheEpochAndRecordsTheBoundary covers
// `relay rotate-key`: the next epoch's key is installed and the
// boundary appended, so the rotation is both usable and auditable.
func TestRotateKey_InstallsTheEpochAndRecordsTheBoundary(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	res, err := lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		FromRSByPeer: map[string]uint64{peerA: 51, peerB: 12},
		Rand:         rand.Reader,
	})
	require.NoError(t, err)
	require.Equal(t, uint16(2), res.KeyEpoch)

	doc, err := metadata.Read(relayDir)
	require.NoError(t, err)
	require.Len(t, doc.KeyEpochs, 2)
	require.Equal(t, uint16(2), doc.KeyEpochs[1].Epoch)
	require.Equal(t, map[string]uint64{peerA: 51, peerB: 12}, doc.KeyEpochs[1].FromRSByPeer)

	// Both epochs remain loadable — that is what D-R10 requires of a
	// reader crossing the boundary.
	ring, err := keyring.Load(keysDir)
	require.NoError(t, err)
	require.Equal(t, []uint16{1, 2}, ring.Epochs())

	first, err := ring.For(1)
	require.NoError(t, err)
	second, err := ring.For(2)
	require.NoError(t, err)
	require.NotEqual(t, first, second, "a rotation must mint new material")
}

// TestRotateKey_IsResumable is the operational case that matters on a
// sneakernet fleet: a rotation interrupted between installing the key
// and recording the boundary must be completable by re-running the
// command, not stuck forever. Both half-states resume.
func TestRotateKey_IsResumable(t *testing.T) {
	t.Run("key installed, boundary not yet recorded", func(t *testing.T) {
		relayDir, keysDir := dirs(t)
		_, err := lifecycle.Init(initReq(relayDir, keysDir))
		require.NoError(t, err)

		// Simulate the crash: epoch 2's key landed, relay.json did not.
		key, err := keyring.Generate(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, keyring.Write(keysDir, 2, key))

		res, err := lifecycle.RotateKey(lifecycle.RotateRequest{
			RelayDir: relayDir, KeysDir: keysDir,
			FromRSByPeer: map[string]uint64{peerA: 51}, Rand: rand.Reader,
		})
		require.NoError(t, err)
		require.Equal(t, uint16(2), res.KeyEpoch)

		// The material already on disk is kept — replacing it would
		// invalidate anything already published under epoch 2.
		ring, err := keyring.Load(keysDir)
		require.NoError(t, err)
		got, err := ring.For(2)
		require.NoError(t, err)
		require.Equal(t, key, got)

		doc, err := metadata.Read(relayDir)
		require.NoError(t, err)
		require.Len(t, doc.KeyEpochs, 2)
	})

	t.Run("a completed rotation re-run is a no-op", func(t *testing.T) {
		relayDir, keysDir := dirs(t)
		_, err := lifecycle.Init(initReq(relayDir, keysDir))
		require.NoError(t, err)
		first, err := lifecycle.RotateKey(lifecycle.RotateRequest{
			RelayDir: relayDir, KeysDir: keysDir,
			FromRSByPeer: map[string]uint64{peerA: 51}, Rand: rand.Reader,
		})
		require.NoError(t, err)

		// Re-running advances to the next epoch rather than disturbing
		// the one already recorded.
		second, err := lifecycle.RotateKey(lifecycle.RotateRequest{
			RelayDir: relayDir, KeysDir: keysDir,
			FromRSByPeer: map[string]uint64{peerA: 90}, Rand: rand.Reader,
		})
		require.NoError(t, err)
		require.Equal(t, first.KeyEpoch+1, second.KeyEpoch)

		doc, err := metadata.Read(relayDir)
		require.NoError(t, err)
		require.Len(t, doc.KeyEpochs, 3)
		require.Equal(t, map[string]uint64{peerA: 51}, doc.KeyEpochs[1].FromRSByPeer,
			"the earlier boundary is untouched")
	})
}

// TestRotateKey_RefusesUnauthenticatedRelays: there is no key to
// rotate, so the command would describe something that cannot happen.
func TestRotateKey_RefusesUnauthenticatedRelays(t *testing.T) {
	relayDir, keysDir := dirs(t)
	req := initReq(relayDir, keysDir)
	req.Authenticated = false
	_, err := lifecycle.Init(req)
	require.NoError(t, err)

	_, err = lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir, Rand: rand.Reader,
	})
	require.Error(t, err)
}

// TestRotateKey_RefusesAnOffGrammarPeer keeps a malformed id out of the
// boundary record, where it would silently never match a peer.
func TestRotateKey_RefusesAnOffGrammarPeer(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	_, err = lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		FromRSByPeer: map[string]uint64{"NOPE": 1}, Rand: rand.Reader,
	})
	require.Error(t, err)
}

// TestRotateKey_RefusesAnUnreadableRelay covers §5.6: corruption blocks
// the operator lifecycle commands.
func TestRotateKey_RefusesAnUnreadableRelay(t *testing.T) {
	relayDir, keysDir := dirs(t)
	require.NoError(t, os.WriteFile(filepath.Join(relayDir, metadata.FileName), []byte("{ broken"), 0o600))

	_, err := lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir, Rand: rand.Reader,
	})
	require.ErrorIs(t, err, metadata.ErrRelayCorrupt)
}

// TestInit_RefusesAnExhaustedEntropySource keeps a weak key out of a
// new relay rather than falling back to something shorter.
func TestInit_RefusesAnExhaustedEntropySource(t *testing.T) {
	relayDir, keysDir := dirs(t)
	req := initReq(relayDir, keysDir)
	req.Rand = failingReader{}

	_, err := lifecycle.Init(req)
	require.Error(t, err)

	// Nothing was left behind for a retry to trip over.
	_, err = metadata.Read(relayDir)
	require.ErrorIs(t, err, metadata.ErrRelayAbsent)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, os.ErrClosed }

// writeRecord plants a relay.json directly, for states the operator
// commands cannot themselves produce.
func writeRecord(t *testing.T, relayDir, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(relayDir, metadata.FileName), []byte(body), 0o600))
}

const recordHead = `{"format_version":1,"relay_id":"r","created_at":"2026-08-22T12:00:00Z",` +
	`"created_by":"0123456789abcdef","projects":[{"project_prefix":"MTIX","first_event_hash":"aaaa1111"}],`

// TestCodeOf maps this package's verdicts to their codes.
func TestCodeOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"mode mismatch", lifecycle.ErrModeMismatch, "RELAY_MODE_MISMATCH"},
		{"history diverged", lifecycle.ErrHistoryDiverged, "RELAY_HISTORY_DIVERGED"},
		{"no shared project", lifecycle.ErrNoSharedProject, "RELAY_NO_SHARED_PROJECT"},
		{"another package's verdict", metadata.ErrRelayCorrupt, ""},
		{"unclassified", os.ErrPermission, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, lifecycle.CodeOf(tt.err))
		})
	}
}

// TestInit_RefusesAnInvalidRecord stops a relay that could not be
// attached to before any key material is minted.
func TestInit_RefusesAnInvalidRecord(t *testing.T) {
	relayDir, keysDir := dirs(t)
	req := initReq(relayDir, keysDir)
	req.Projects = nil

	_, err := lifecycle.Init(req)
	require.Error(t, err)
	_, err = keyring.Load(keysDir)
	require.ErrorIs(t, err, keyring.ErrKeyAbsent, "no key is minted for a relay that cannot exist")
}

// TestInit_UnwritableKeyDirectoryIsReported covers the medium refusing
// the key before the record is written, which is the ordering that
// leaves nothing half-created behind.
func TestInit_UnwritableKeyDirectoryIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	relayDir, _ := dirs(t)
	locked := t.TempDir()
	require.NoError(t, os.Chmod(locked, 0o500))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	req := initReq(relayDir, filepath.Join(locked, "keys"))
	_, err := lifecycle.Init(req)
	require.Error(t, err)

	_, err = metadata.Read(relayDir)
	require.ErrorIs(t, err, metadata.ErrRelayAbsent, "no record is left for a relay with no key")
}

// TestInit_UnreachableRelayDirectoryIsReported covers the existence
// pre-check failing for a reason other than absence.
func TestInit_UnreachableRelayDirectoryIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	root := t.TempDir()
	relayDir := filepath.Join(root, "relay")
	require.NoError(t, os.Mkdir(relayDir, 0o700))
	require.NoError(t, os.Chmod(root, 0o000))
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	_, err := lifecycle.Init(initReq(relayDir, filepath.Join(root, "keys")))
	require.Error(t, err)
	require.NotErrorIs(t, err, metadata.ErrRelayExists)
}

// TestAttach_AuthenticatedRelayWithNoRecordedEpoch is a record that
// claims authentication but names no key epoch — internally
// inconsistent, so attach refuses rather than guessing an epoch.
func TestAttach_AuthenticatedRelayWithNoRecordedEpoch(t *testing.T) {
	relayDir, keysDir := dirs(t)
	writeRecord(t, relayDir, recordHead+`"authenticated":true,"key_epochs":[],"retired_peers":[]}`)

	_, err := lifecycle.Attach(lifecycle.AttachRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		Local:         []metadata.Project{mtixProject()},
		Authenticated: true,
	})
	require.ErrorIs(t, err, metadata.ErrRelayCorrupt)
}

// TestAttach_ReportsAKeyDirectoryProblem keeps a permissions failure on
// the key store distinct from the key simply being absent.
func TestAttach_ReportsAKeyDirectoryProblem(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)
	require.NoError(t, os.Chmod(keysDir, 0o755))
	t.Cleanup(func() { _ = os.Chmod(keysDir, 0o700) })

	_, err = lifecycle.Attach(lifecycle.AttachRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		Local:         []metadata.Project{mtixProject()},
		Authenticated: true,
	})
	require.ErrorIs(t, err, keyring.ErrKeyPerms)
}

// TestRotateKey_RefusesAtTheLastEpoch is the end of the road the frame
// can express: key_epoch is a 16-bit field, so there is no epoch after
// 65535 and the command says so instead of wrapping to zero, which
// would re-point readers at the relay's very first key.
func TestRotateKey_RefusesAtTheLastEpoch(t *testing.T) {
	relayDir, keysDir := dirs(t)
	writeRecord(t, relayDir, recordHead+
		`"authenticated":true,"key_epochs":[{"epoch":65535}],"retired_peers":[]}`)

	_, err := lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir, Rand: rand.Reader,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "65535")
}

// TestRotateKey_AuthenticatedRelayWithNoRecordedEpoch mirrors the
// attach case: nothing to rotate from.
func TestRotateKey_AuthenticatedRelayWithNoRecordedEpoch(t *testing.T) {
	relayDir, keysDir := dirs(t)
	writeRecord(t, relayDir, recordHead+`"authenticated":true,"key_epochs":[],"retired_peers":[]}`)

	_, err := lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir, Rand: rand.Reader,
	})
	require.ErrorIs(t, err, metadata.ErrRelayCorrupt)
}

// TestRotateKey_ReportsAKeyDirectoryProblem covers a key store that is
// present but not usable — refused rather than treated as absent, which
// would mint a second key for an epoch that may already have one.
func TestRotateKey_ReportsAKeyDirectoryProblem(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)
	require.NoError(t, os.Chmod(keysDir, 0o755))
	t.Cleanup(func() { _ = os.Chmod(keysDir, 0o700) })

	_, err = lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir, Rand: rand.Reader,
	})
	require.ErrorIs(t, err, keyring.ErrKeyPerms)
}

// TestRotateKey_RefusesAnExhaustedEntropySource keeps a weak key out of
// a rotation, and leaves the recorded history untouched.
func TestRotateKey_RefusesAnExhaustedEntropySource(t *testing.T) {
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	_, err = lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir, Rand: failingReader{},
	})
	require.Error(t, err)

	doc, err := metadata.Read(relayDir)
	require.NoError(t, err)
	require.Len(t, doc.KeyEpochs, 1, "no boundary is recorded for a rotation that never happened")
}

// TestRotateKey_UnwritableRecordLeavesTheKeyInstalled documents the
// half-state the resumable path is built for: the key landed, the
// boundary did not, and re-running finishes the job rather than
// starting a second rotation.
func TestRotateKey_UnwritableRecordLeavesTheKeyInstalled(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	relayDir, keysDir := dirs(t)
	_, err := lifecycle.Init(initReq(relayDir, keysDir))
	require.NoError(t, err)

	path := filepath.Join(relayDir, metadata.FileName)
	require.NoError(t, os.Chmod(path, 0o400))

	_, err = lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		FromRSByPeer: map[string]uint64{peerA: 51}, Rand: rand.Reader,
	})
	require.Error(t, err)

	ring, err := keyring.Load(keysDir)
	require.NoError(t, err)
	installed, err := ring.For(2)
	require.NoError(t, err)

	// The record becomes writable again and the retry completes,
	// keeping the key already published under.
	require.NoError(t, os.Chmod(path, 0o600))
	res, err := lifecycle.RotateKey(lifecycle.RotateRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		FromRSByPeer: map[string]uint64{peerA: 51}, Rand: rand.Reader,
	})
	require.NoError(t, err)
	require.Equal(t, uint16(2), res.KeyEpoch)

	ring, err = keyring.Load(keysDir)
	require.NoError(t, err)
	after, err := ring.For(2)
	require.NoError(t, err)
	require.Equal(t, installed, after, "a retry must not replace material already in use")
}
