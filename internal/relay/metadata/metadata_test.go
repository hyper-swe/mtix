// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package metadata_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/stretchr/testify/require"
)

const (
	peerA = "0123456789abcdef"
	peerB = "fedcba9876543210"
)

var fixedTime = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func initConfig() metadata.InitConfig {
	return metadata.InitConfig{
		RelayID:   "01a0238b-d7d5-77cc-95c6-98a472ed7803",
		CreatedAt: fixedTime,
		CreatedBy: peerA,
		Projects: []metadata.Project{
			{Prefix: "MTIX", FirstEventHash: "aaaa1111"},
		},
		Authenticated: true,
	}
}

// TestInit_WritesTheRelayRecord covers `relay init`. Identity and time
// arrive from the caller — this layer mints neither, which keeps it
// stdlib-only and its output reproducible in a test.
func TestInit_WritesTheRelayRecord(t *testing.T) {
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)

	require.Equal(t, metadata.FormatVersion, doc.FormatVersion)
	require.Equal(t, "01a0238b-d7d5-77cc-95c6-98a472ed7803", doc.RelayID)
	require.Equal(t, fixedTime, doc.CreatedAt.UTC())
	require.Equal(t, peerA, doc.CreatedBy)
	require.True(t, doc.Authenticated)
	require.Equal(t, []metadata.Project{{Prefix: "MTIX", FirstEventHash: "aaaa1111"}}, doc.Projects)

	// An authenticated relay starts at key epoch 1, with the boundary
	// recorded from the start of every peer's stream.
	require.Len(t, doc.KeyEpochs, 1)
	require.Equal(t, uint16(1), doc.KeyEpochs[0].Epoch)
	require.Empty(t, doc.RetiredPeers)
}

// TestInit_UnauthenticatedRelayRecordsNoKeyEpoch is FR-21 §8.3: the
// opt-out is explicit at init and recorded in the file, so a later
// attach can refuse a mode mismatch instead of inferring one.
func TestInit_UnauthenticatedRelayRecordsNoKeyEpoch(t *testing.T) {
	cfg := initConfig()
	cfg.Authenticated = false

	doc, err := metadata.Init(cfg)
	require.NoError(t, err)
	require.False(t, doc.Authenticated)
	require.Empty(t, doc.KeyEpochs, "an unauthenticated relay has no key epochs to record")
}

// TestInit_Validation refuses a record that could not be attached to.
func TestInit_Validation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*metadata.InitConfig)
	}{
		{"no relay id", func(c *metadata.InitConfig) { c.RelayID = "" }},
		{"no creator", func(c *metadata.InitConfig) { c.CreatedBy = "" }},
		{"creator off the peer grammar", func(c *metadata.InitConfig) { c.CreatedBy = "NOPE" }},
		{"no projects", func(c *metadata.InitConfig) { c.Projects = nil }},
		{"project without a prefix", func(c *metadata.InitConfig) {
			c.Projects = []metadata.Project{{FirstEventHash: "aaaa1111"}}
		}},
		{"project without a first event hash", func(c *metadata.InitConfig) {
			c.Projects = []metadata.Project{{Prefix: "MTIX"}}
		}},
		{"duplicate project prefix", func(c *metadata.InitConfig) {
			c.Projects = []metadata.Project{
				{Prefix: "MTIX", FirstEventHash: "aaaa1111"},
				{Prefix: "MTIX", FirstEventHash: "bbbb2222"},
			}
		}},
		{"zero creation time", func(c *metadata.InitConfig) { c.CreatedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := initConfig()
			tt.mutate(&cfg)
			_, err := metadata.Init(cfg)
			require.Error(t, err)
		})
	}
}

// TestWriteRead_RoundTrip covers the file on the medium.
func TestWriteRead_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, metadata.Write(dir, doc))

	got, err := metadata.Read(dir)
	require.NoError(t, err)
	require.Equal(t, doc.RelayID, got.RelayID)
	require.Equal(t, doc.CreatedBy, got.CreatedBy)
	require.Equal(t, doc.Projects, got.Projects)
	require.Equal(t, doc.Authenticated, got.Authenticated)
	require.Equal(t, doc.KeyEpochs, got.KeyEpochs)
	require.True(t, doc.CreatedAt.Equal(got.CreatedAt))
}

// TestWrite_RefusesToOverwrite is the §5.6 immutability rule at the
// file boundary: relay.json is written once at init. Everything after
// that appends through the two permitted fields.
func TestWrite_RefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, metadata.Write(dir, doc))
	require.ErrorIs(t, metadata.Write(dir, doc), metadata.ErrRelayExists)
}

// TestRead_MissingFile lets a caller tell "not a relay directory" from
// a damaged one.
func TestRead_MissingFile(t *testing.T) {
	_, err := metadata.Read(t.TempDir())
	require.ErrorIs(t, err, metadata.ErrRelayAbsent)
	require.Equal(t, "RELAY_META_ABSENT", metadata.CodeOf(err))
}

// TestRead_CorruptFile is §5.6's failure mode. Corruption here blocks
// attach and the operator commands, and the verdict says so — data flow
// between already-attached peers does not consult this file, because
// each verified a copy into its own config at attach time.
func TestRead_CorruptFile(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"truncated json", `{"format_version":1,`},
		{"not json", "this is not json"},
		{"json array", `[]`},
		{"no relay id", `{"format_version":1,"created_by":"0123456789abcdef"}`},
		{"creator off grammar", `{"format_version":1,"relay_id":"x","created_by":"NOPE","projects":[{"project_prefix":"MTIX","first_event_hash":"a"}]}`},
		{"no projects", `{"format_version":1,"relay_id":"x","created_by":"0123456789abcdef","projects":[]}`},
		{"a format major this build cannot read", `{"format_version":99,"relay_id":"x","created_by":"0123456789abcdef","projects":[{"project_prefix":"MTIX","first_event_hash":"a"}]}`},
		{"duplicate project prefix", `{"format_version":1,"relay_id":"x","created_by":"0123456789abcdef","projects":[{"project_prefix":"MTIX","first_event_hash":"a"},{"project_prefix":"MTIX","first_event_hash":"b"}]}`},
		{"project with no first event hash", `{"format_version":1,"relay_id":"x","created_by":"0123456789abcdef","projects":[{"project_prefix":"MTIX"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, metadata.FileName), []byte(tt.body), 0o600))
			_, err := metadata.Read(dir)
			require.ErrorIs(t, err, metadata.ErrRelayCorrupt)
			require.Equal(t, "RELAY_META_CORRUPT", metadata.CodeOf(err))
		})
	}
}

// TestRead_RefusesASymlink applies the CWE-59 discipline to the one
// file that decides what a joining peer trusts.
func TestRead_RefusesASymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "relay")
	require.NoError(t, os.Mkdir(dir, 0o700))
	target := filepath.Join(root, "elsewhere.json")
	require.NoError(t, os.WriteFile(target, []byte("{}"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, metadata.FileName)))

	_, err := metadata.Read(dir)
	require.ErrorIs(t, err, metadata.ErrRelaySymlink)
}

// TestAppendKeyEpoch is the D-R10 boundary record: rotation appends the
// epoch and the relay sequence it takes effect from, per peer. Nothing
// rewrites history — §5.6 permits appending to exactly two fields.
func TestAppendKeyEpoch(t *testing.T) {
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)

	require.NoError(t, doc.AppendKeyEpoch(2, map[string]uint64{peerA: 51, peerB: 12}))
	require.Len(t, doc.KeyEpochs, 2)
	require.Equal(t, uint16(2), doc.KeyEpochs[1].Epoch)
	require.Equal(t, map[string]uint64{peerA: 51, peerB: 12}, doc.KeyEpochs[1].FromRSByPeer)

	require.NoError(t, doc.AppendKeyEpoch(3, map[string]uint64{peerA: 99}))
	require.Equal(t, []uint16{1, 2, 3},
		[]uint16{doc.KeyEpochs[0].Epoch, doc.KeyEpochs[1].Epoch, doc.KeyEpochs[2].Epoch})
}

// TestAppendKeyEpoch_RefusesRewrites keeps the boundary history
// append-only. Re-stating an epoch at a different relay sequence would
// move a boundary readers have already crossed, which is the one way a
// rotation record could lie.
func TestAppendKeyEpoch_RefusesRewrites(t *testing.T) {
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, doc.AppendKeyEpoch(2, map[string]uint64{peerA: 51}))

	tests := []struct {
		name  string
		epoch uint16
	}{
		{"the epoch already recorded", 2},
		{"an earlier epoch", 1},
		{"zero", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := doc.AppendKeyEpoch(tt.epoch, map[string]uint64{peerA: 99})
			require.ErrorIs(t, err, metadata.ErrEpochNotForward)
			require.Len(t, doc.KeyEpochs, 2, "a refusal must leave the record untouched")
		})
	}
}

// TestAppendKeyEpoch_RefusesUnauthenticatedRelays: there is no key to
// rotate, so recording a boundary would describe something that cannot
// happen.
func TestAppendKeyEpoch_RefusesUnauthenticatedRelays(t *testing.T) {
	cfg := initConfig()
	cfg.Authenticated = false
	doc, err := metadata.Init(cfg)
	require.NoError(t, err)

	require.Error(t, doc.AppendKeyEpoch(2, map[string]uint64{peerA: 1}))
}

// TestAppendKeyEpoch_RefusesAnOffGrammarPeer keeps a malformed id out
// of the boundary record, where it would silently never match a peer.
func TestAppendKeyEpoch_RefusesAnOffGrammarPeer(t *testing.T) {
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.Error(t, doc.AppendKeyEpoch(2, map[string]uint64{"NOPE": 1}))
}

// TestCurrentKeyEpoch reports the epoch a publisher should write under
// according to the record, which the keyring must agree with.
func TestCurrentKeyEpoch(t *testing.T) {
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	epoch, ok := doc.CurrentKeyEpoch()
	require.True(t, ok)
	require.Equal(t, uint16(1), epoch)

	require.NoError(t, doc.AppendKeyEpoch(7, map[string]uint64{peerA: 3}))
	epoch, ok = doc.CurrentKeyEpoch()
	require.True(t, ok)
	require.Equal(t, uint16(7), epoch)

	cfg := initConfig()
	cfg.Authenticated = false
	plain, err := metadata.Init(cfg)
	require.NoError(t, err)
	_, ok = plain.CurrentKeyEpoch()
	require.False(t, ok)
}

// TestRewrite_PersistsAnAppendOnlyChange covers the operator-command
// write-back path, which is the only write after init.
func TestRewrite_PersistsAnAppendOnlyChange(t *testing.T) {
	dir := t.TempDir()
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, metadata.Write(dir, doc))

	require.NoError(t, doc.AppendKeyEpoch(2, map[string]uint64{peerA: 51}))
	require.NoError(t, metadata.Rewrite(dir, doc))

	got, err := metadata.Read(dir)
	require.NoError(t, err)
	require.Len(t, got.KeyEpochs, 2)
	require.Equal(t, uint16(2), got.KeyEpochs[1].Epoch)
	require.Equal(t, map[string]uint64{peerA: 51}, got.KeyEpochs[1].FromRSByPeer)
}

// TestRewrite_RefusesToChangeTheImmutableFields is the §5.6 invariant
// enforced where it can actually be checked: key_epochs and
// retired_peers may grow, and nothing else may move.
func TestRewrite_RefusesToChangeTheImmutableFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*metadata.Relay)
	}{
		{"relay id", func(d *metadata.Relay) { d.RelayID = "different" }},
		{"creator", func(d *metadata.Relay) { d.CreatedBy = peerB }},
		{"creation time", func(d *metadata.Relay) { d.CreatedAt = fixedTime.Add(time.Hour) }},
		{"authentication mode", func(d *metadata.Relay) { d.Authenticated = false }},
		{"project set", func(d *metadata.Relay) {
			d.Projects = append(d.Projects, metadata.Project{Prefix: "OTHER", FirstEventHash: "bbbb"})
		}},
		{"a project's first event hash", func(d *metadata.Relay) { d.Projects[0].FirstEventHash = "cccc" }},
		{"format version", func(d *metadata.Relay) { d.FormatVersion = 99 }},
		{"a recorded key epoch boundary", func(d *metadata.Relay) {
			d.KeyEpochs[0].FromRSByPeer = map[string]uint64{peerA: 777}
		}},
		{"dropping a key epoch", func(d *metadata.Relay) { d.KeyEpochs = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			doc, err := metadata.Init(initConfig())
			require.NoError(t, err)
			require.NoError(t, metadata.Write(dir, doc))

			tt.mutate(doc)
			err = metadata.Rewrite(dir, doc)
			require.ErrorIs(t, err, metadata.ErrRelayImmutable)

			// The file on the medium is untouched.
			onDisk, readErr := metadata.Read(dir)
			require.NoError(t, readErr)
			require.Equal(t, "01a0238b-d7d5-77cc-95c6-98a472ed7803", onDisk.RelayID)
		})
	}
}

// TestWrite_ProducesStableCanonicalJSON keeps the file diffable and its
// key order deterministic across writers.
func TestWrite_ProducesStableCanonicalJSON(t *testing.T) {
	dir := t.TempDir()
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, doc.AppendKeyEpoch(2, map[string]uint64{peerB: 12, peerA: 51}))
	require.NoError(t, metadata.Write(dir, doc))

	raw, err := os.ReadFile(filepath.Join(dir, metadata.FileName))
	require.NoError(t, err)
	require.True(t, json.Valid(raw))

	// Writing the same record again must produce identical bytes.
	other := t.TempDir()
	require.NoError(t, metadata.Write(other, doc))
	raw2, err := os.ReadFile(filepath.Join(other, metadata.FileName))
	require.NoError(t, err)
	require.Equal(t, string(raw), string(raw2))
}

// TestCodeOf maps verdicts to the codes status and doctor surface.
func TestCodeOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"absent", metadata.ErrRelayAbsent, "RELAY_META_ABSENT"},
		{"corrupt", metadata.ErrRelayCorrupt, "RELAY_META_CORRUPT"},
		{"symlink", metadata.ErrRelaySymlink, "RELAY_META_SYMLINK"},
		{"exists is not a verdict", metadata.ErrRelayExists, ""},
		{"immutable is not a verdict", metadata.ErrRelayImmutable, ""},
		{"epoch refusal is not a verdict", metadata.ErrEpochNotForward, ""},
		{"unclassified", os.ErrPermission, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, metadata.CodeOf(tt.err))
		})
	}
}

// TestRead_NotARegularFile covers a directory standing where the record
// belongs — refused before anything tries to parse it.
func TestRead_NotARegularFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, metadata.FileName), 0o700))

	_, err := metadata.Read(dir)
	require.ErrorIs(t, err, metadata.ErrRelayCorrupt)
}

// TestRead_UnreachableDirectoryIsAnIOError keeps a permission problem
// on the path distinct from a verdict about the record.
func TestRead_UnreachableDirectoryIsAnIOError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "relay")
	require.NoError(t, os.Mkdir(dir, 0o700))
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, metadata.Write(dir, doc))
	require.NoError(t, os.Chmod(root, 0o000))
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	_, err = metadata.Read(dir)
	require.Error(t, err)
	require.Equal(t, "", metadata.CodeOf(err), "an I/O failure is not a record verdict")
}

// TestRead_UnreadableRecordIsAnIOError covers the file itself being
// unreadable after it was found.
func TestRead_UnreadableRecordIsAnIOError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir := t.TempDir()
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, metadata.Write(dir, doc))
	path := filepath.Join(dir, metadata.FileName)
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err = metadata.Read(dir)
	require.Error(t, err)
	require.Equal(t, "", metadata.CodeOf(err))
}

// TestWrite_RefusesAnInvalidRecord stops a malformed record before it
// reaches the medium, where a joining peer would have to make sense
// of it.
func TestWrite_RefusesAnInvalidRecord(t *testing.T) {
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	doc.RelayID = ""
	require.Error(t, metadata.Write(t.TempDir(), doc))
}

// TestWrite_UnwritableDirectoryIsReported covers the medium refusing
// the create for a reason other than the record already existing.
func TestWrite_UnwritableDirectoryIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	err = metadata.Write(dir, doc)
	require.Error(t, err)
	require.NotErrorIs(t, err, metadata.ErrRelayExists)
}

// TestRewrite_UnwritableRecordIsReported covers the append-only write
// failing on the medium after passing every policy check.
func TestRewrite_UnwritableRecordIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir := t.TempDir()
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, metadata.Write(dir, doc))
	require.NoError(t, doc.AppendKeyEpoch(2, map[string]uint64{peerA: 51}))

	path := filepath.Join(dir, metadata.FileName)
	require.NoError(t, os.Chmod(path, 0o400))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	require.Error(t, metadata.Rewrite(dir, doc))
}

// TestRewrite_WithoutAnExistingRecord refuses rather than creating one:
// an append to a record that is not there would invent a relay identity
// no peer ever agreed to.
func TestRewrite_WithoutAnExistingRecord(t *testing.T) {
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.ErrorIs(t, metadata.Rewrite(t.TempDir(), doc), metadata.ErrRelayAbsent)
}

// TestRewrite_RefusesAChangedBoundaryPosition closes the subtle half of
// the append-only rule: an epoch whose recorded relay sequence moved,
// with the epoch number and the entry count both unchanged. That is the
// edit that would silently relocate a boundary readers already crossed.
func TestRewrite_RefusesAChangedBoundaryPosition(t *testing.T) {
	dir := t.TempDir()
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, doc.AppendKeyEpoch(2, map[string]uint64{peerA: 51, peerB: 12}))
	require.NoError(t, metadata.Write(dir, doc))

	tests := []struct {
		name   string
		mutate func(*metadata.Relay)
	}{
		{"a peer's position moved", func(d *metadata.Relay) { d.KeyEpochs[1].FromRSByPeer[peerA] = 999 }},
		{"a peer dropped from the boundary", func(d *metadata.Relay) { delete(d.KeyEpochs[1].FromRSByPeer, peerB) }},
		{"a peer added to the boundary", func(d *metadata.Relay) { d.KeyEpochs[1].FromRSByPeer["aaaaaaaaaaaaaaaa"] = 1 }},
		{"the epoch number changed", func(d *metadata.Relay) { d.KeyEpochs[1].Epoch = 5 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edited, err := metadata.Read(dir)
			require.NoError(t, err)
			tt.mutate(edited)
			require.ErrorIs(t, metadata.Rewrite(dir, edited), metadata.ErrRelayImmutable)
		})
	}
}

// TestRewrite_AcceptsAGrowingRetiredRoster covers the other
// append-permitted field, so a later deliverable can use it without
// revisiting this rule.
func TestRewrite_AcceptsAGrowingRetiredRoster(t *testing.T) {
	dir := t.TempDir()
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, metadata.Write(dir, doc))

	doc.RetiredPeers = append(doc.RetiredPeers, peerB)
	require.NoError(t, metadata.Rewrite(dir, doc))

	got, err := metadata.Read(dir)
	require.NoError(t, err)
	require.Equal(t, []string{peerB}, got.RetiredPeers)

	t.Run("and a shrinking one, because a peer clears its own entry when it rejoins", func(t *testing.T) {
		// FR-21 §6.7: a retired peer that returns re-enters through
		// bootstrap, which clears its retirement. retired_peers is a
		// live roster, unlike key_epochs, which records where a
		// rotation boundary fell and may never move.
		rejoined, err := metadata.Read(dir)
		require.NoError(t, err)
		rejoined.RetiredPeers = nil
		require.NoError(t, metadata.Rewrite(dir, rejoined))

		after, err := metadata.Read(dir)
		require.NoError(t, err)
		require.Empty(t, after.RetiredPeers)
	})
	t.Run("but never a malformed peer id", func(t *testing.T) {
		edited, err := metadata.Read(dir)
		require.NoError(t, err)
		edited.RetiredPeers = []string{"NOT A PEER"}
		require.ErrorIs(t, metadata.Rewrite(dir, edited), metadata.ErrRelayImmutable)
	})
}
