// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests relay doctor verdicts and recovery hints for configuration, metadata,
// keys, publishing, peer activity, poll cadence, snapshots and privacy advisories.
package main

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
)

const diagnosticSelf = "0123456789abcdef-self"
const diagnosticOther = "1123456789abcdef-other"
const diagnosticRetired = "2123456789abcdef-retired"

// initDiagnosticRelay confines configuration, metadata and generated keys to test-owned paths.
func initDiagnosticRelay(t *testing.T, authenticated bool) (string, *metadata.Relay) {
	t.Helper()
	initTestApp(t)
	dir := t.TempDir()
	setDiagnosticConfig(t, "sync.relay.dir", dir)
	setDiagnosticConfig(t, "sync.relay.peer_id", diagnosticSelf)
	doc, err := metadata.Init(metadata.InitConfig{
		RelayID: "diagnostic-relay", CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		CreatedBy: diagnosticSelf, Projects: []metadata.Project{{Prefix: "TEST", FirstEventHash: "fixture-history"}},
		Authenticated: authenticated,
	})
	require.NoError(t, err)
	require.NoError(t, metadata.Write(dir, doc))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, tick.PeersDirName), 0o700))
	if authenticated {
		key, err := keyring.Generate(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, keyring.Write(relayKeysDir(), 1, key))
	}
	return dir, doc
}

func setDiagnosticConfig(t *testing.T, key, value string) {
	t.Helper()
	_, err := app.configSvc.Set(key, value)
	require.NoError(t, err)
}

func diagnosticSegment(t *testing.T, dir, peer string, no uint64, stamp time.Time) string {
	t.Helper()
	segDir := filepath.Join(dir, tick.PeersDirName, peer, tick.SegmentsDirName)
	require.NoError(t, os.MkdirAll(segDir, 0o700))
	path := filepath.Join(segDir, segment.FileName(no))
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, os.Chtimes(path, stamp, stamp))
	return path
}

func diagnosticSnapshot(t *testing.T, dir, name string, stamp time.Time) string {
	t.Helper()
	root := filepath.Join(dir, bootstrap.DirName)
	require.NoError(t, os.MkdirAll(root, 0o700))
	path := filepath.Join(root, name)
	require.NoError(t, os.WriteFile(path, []byte("fixture snapshot"), 0o600))
	require.NoError(t, os.Chtimes(path, stamp, stamp))
	return path
}

func diagnosticOld() time.Time     { return time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC) }
func diagnosticCurrent() time.Time { return time.Date(2100, 1, 1, 12, 0, 0, 0, time.UTC) }

func TestAppendRelayChecks_Unconfigured_IsHealthy(t *testing.T) {
	saveAndResetApp(t)
	got := appendRelayChecks(t.Context(), DoctorReport{OverallPass: true})
	require.Equal(t, DoctorReport{OverallPass: true, Checks: []DoctorCheck{{
		Name: "relay configured", Pass: true, Detail: "no relay configured (sync.relay.dir unset)",
	}}}, got)
}

func TestAppendRelayChecks_AuthenticatedRelay_ReportsEveryCheck(t *testing.T) {
	dir, _ := initDiagnosticRelay(t, true)
	require.NoError(t, runCreate("Publishable task", "", "", 3, "", "", "", "", ""))
	got := appendRelayChecks(t.Context(), DoctorReport{OverallPass: true})
	require.True(t, got.OverallPass)
	require.Equal(t, []DoctorCheck{
		{Name: "relay reachable", Pass: true, Detail: dir},
		{Name: "relay record", Pass: true, Detail: "relay diagnostic-relay, authenticated (experimental)"},
		{Name: "relay key", Pass: true, Detail: "key epoch 1 present, mode 0600"},
		{Name: "relay publishing", Pass: true, Detail: "publishing"},
		{Name: "relay peers", Pass: true, Detail: "1 peer(s), 0 retired"},
		{Name: "relay poll cadence", Pass: true, Detail: "poll every 5s"},
		{Name: "relay snapshots", Pass: true, Detail: "no stale bootstrap snapshots"},
		{Name: "relay privacy", Pass: true, Detail: "no privacy advisories"},
	}, got.Checks)
	pos, err := app.store.RelayPushCursor(t.Context())
	require.NoError(t, err)
	require.Greater(t, pos.NextRS, uint64(1), "doctor must actually publish pending work")
}

func TestAppendRelayChecks_InvalidMedium_StopsWithRecovery(t *testing.T) {
	tests := []struct {
		name, code, hint string
		metadataOnly     bool
	}{
		{"missing directory", "", "mount the relay", false},
		{"root symlink", "RELAY_SYMLINK", "point sync.relay.dir at the real directory", false},
		{"peers symlink", "RELAY_SYMLINK", "remove the offending entry", false},
		{"peers file", "RELAY_FOREIGN_ENTRY", "remove the offending entry", false},
		{"missing record", "RELAY_META_ABSENT", "check that the relay directory is reachable", true},
		{"malformed record", "RELAY_META_CORRUPT", "check that the relay directory is reachable", true},
		{"record symlink", "RELAY_META_SYMLINK", "check that the relay directory is reachable", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, _ := initDiagnosticRelay(t, true)
			switch tt.name {
			case "missing directory":
				setDiagnosticConfig(t, "sync.relay.dir", filepath.Join(dir, "absent"))
			case "root symlink":
				link := filepath.Join(t.TempDir(), "relay-link")
				require.NoError(t, os.Symlink(dir, link))
				setDiagnosticConfig(t, "sync.relay.dir", link)
			case "peers symlink", "peers file":
				path := filepath.Join(dir, tick.PeersDirName)
				require.NoError(t, os.Remove(path))
				if tt.name == "peers symlink" {
					require.NoError(t, os.Symlink(t.TempDir(), path))
				} else {
					require.NoError(t, os.WriteFile(path, nil, 0o600))
				}
			case "missing record", "record symlink":
				path := filepath.Join(dir, metadata.FileName)
				require.NoError(t, os.Remove(path))
				if tt.name == "record symlink" {
					target := filepath.Join(t.TempDir(), "record")
					require.NoError(t, os.WriteFile(target, []byte("unread"), 0o600))
					require.NoError(t, os.Symlink(target, path))
				}
			case "malformed record":
				require.NoError(t, os.WriteFile(filepath.Join(dir, metadata.FileName), []byte("{"), 0o644))
			}
			got := appendRelayChecks(t.Context(), DoctorReport{OverallPass: true})
			require.False(t, got.OverallPass)
			wantRows := 1
			if tt.metadataOnly {
				wantRows = 2
				require.True(t, got.Checks[0].Pass)
			}
			require.Len(t, got.Checks, wantRows, "later checks must not run on an invalid medium")
			last := got.Checks[wantRows-1]
			require.False(t, last.Pass)
			require.Contains(t, last.Detail, tt.hint)
			if tt.code != "" {
				require.Contains(t, last.Detail, tt.code)
			}
		})
	}
}

func TestCheckRelayKey_ModeAndEpoch_ReportsUsableKey(t *testing.T) {
	tests := []struct {
		name, detail string
		pass         bool
	}{
		{"present", "key epoch 1 present, mode 0600", true},
		{"missing epoch", "RELAY_KEY_ABSENT: no key for the requested epoch: epoch 2 (present: 1)", false},
		{"no epochs", "RELAY_META_CORRUPT: relay claims authentication but records no key epoch", false},
		{"missing ring", "RELAY_KEY_ABSENT", false},
		{"wrong permissions", "RELAY_KEY_PERMS", false},
		{"invalid material", "RELAY_KEY_INVALID", false},
		{"symlink key", "RELAY_KEY_SYMLINK", false},
		{"unauthenticated", "relay is unauthenticated (§8.3 opt-out); records carry no MAC", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, doc := initDiagnosticRelay(t, tt.name != "unauthenticated")
			keyPath := filepath.Join(relayKeysDir(), "1")
			switch tt.name {
			case "missing epoch":
				doc.KeyEpochs = append(doc.KeyEpochs, metadata.KeyEpochBoundary{Epoch: 2})
			case "no epochs":
				doc.KeyEpochs = nil
			case "missing ring":
				app.mtixDir = t.TempDir()
			case "wrong permissions":
				require.NoError(t, os.Chmod(keyPath, 0o644))
			case "invalid material":
				require.NoError(t, os.WriteFile(keyPath, []byte("invalid"), 0o600))
			case "symlink key":
				require.NoError(t, os.Remove(keyPath))
				require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), keyPath))
			}
			pass, detail := checkRelayKey(doc)
			require.Equal(t, tt.pass, pass)
			require.Contains(t, detail, tt.detail)
			if !pass && tt.name != "no epochs" {
				require.Contains(t, detail, "fix:")
			}
		})
	}
}

func TestCheckRelayPublishing_Failures_AreReported(t *testing.T) {
	for _, name := range []string{"no store", "missing metadata", "closed store"} {
		t.Run(name, func(t *testing.T) {
			dir, _ := initDiagnosticRelay(t, true)
			switch name {
			case "no store":
				saved := app.store
				app.store = nil
				t.Cleanup(func() { app.store = saved })
			case "missing metadata":
				require.NoError(t, os.Remove(filepath.Join(dir, metadata.FileName)))
			case "closed store":
				require.NoError(t, app.store.Close())
			}
			pass, detail := checkRelayPublishing(t.Context())
			require.False(t, pass)
			switch name {
			case "no store":
				require.Equal(t, "local store not initialized", detail)
			case "missing metadata":
				require.Contains(t, detail, "RELAY_META_ABSENT")
				require.Contains(t, detail, "fix:")
			case "closed store":
				require.Contains(t, detail, "read relay push cursor")
			}
		})
	}
}

func TestCheckRelayPeers_ActiveRetiredAndForeign_ReportsRoster(t *testing.T) {
	for _, silent := range []bool{false, true} {
		t.Run(strconv.FormatBool(silent), func(t *testing.T) {
			dir, doc := initDiagnosticRelay(t, true)
			diagnosticSegment(t, dir, diagnosticSelf, 1, diagnosticCurrent())
			stamp := diagnosticCurrent()
			if silent {
				stamp = diagnosticOld()
			}
			diagnosticSegment(t, dir, diagnosticOther, 1, stamp)
			diagnosticSegment(t, dir, diagnosticRetired, 1, diagnosticOld())
			doc.RetiredPeers = []string{diagnosticRetired}
			require.NoError(t, os.WriteFile(filepath.Join(dir, tick.PeersDirName, "notes.txt"), nil, 0o600))
			pass, detail := checkRelayPeers(dir, doc)
			require.Equal(t, !silent, pass)
			if silent {
				require.Contains(t, detail, "1 peer(s) silent past the threshold: "+diagnosticOther)
				require.Contains(t, detail, "mtix sync relay retire-peer <id>")
				require.Contains(t, detail, "mtix sync relay clone")
				require.NotContains(t, detail, diagnosticRetired)
			} else {
				require.Equal(t, "3 peer(s), 1 retired; RELAY_FOREIGN_ENTRY: ignored notes.txt", detail)
			}
		})
	}
	t.Run("unseen peer", func(t *testing.T) {
		dir, doc := initDiagnosticRelay(t, true)
		require.NoError(t, os.Mkdir(filepath.Join(dir, tick.PeersDirName, diagnosticOther), 0o700))
		pass, detail := checkRelayPeers(dir, doc)
		require.False(t, pass)
		require.Contains(t, detail, "1 peer(s) silent past the threshold: "+diagnosticOther)
	})
	t.Run("invalid peers directory", func(t *testing.T) {
		dir, doc := initDiagnosticRelay(t, true)
		require.NoError(t, os.Remove(filepath.Join(dir, tick.PeersDirName)))
		pass, detail := checkRelayPeers(dir, doc)
		require.False(t, pass)
		require.Contains(t, detail, "lstat")
	})
}

func TestCheckRelayPollCadence_Intervals_ReportConvergence(t *testing.T) {
	tests := []struct {
		name, poll, detail string
		pass               bool
	}{
		{"default", "", "poll every 5s", true},
		{"tick only", "0", "tick-only peer (poll_interval 0); converges on `mtix sync relay tick`", true},
		{"equal threshold", "1209600", "poll every 1209600s", true},
		{"excessive", "1209601", "poll_interval (1209601s) exceeds the silence threshold (14d)", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			if tt.poll != "" {
				setDiagnosticConfig(t, "sync.relay.poll_interval", tt.poll)
			}
			pass, detail := checkRelayPollCadence()
			require.Equal(t, tt.pass, pass)
			require.Contains(t, detail, tt.detail)
			if !pass {
				require.Contains(t, detail, "fix: lower sync.relay.poll_interval, or raise sync.relay.silent_peer_days")
			}
		})
	}
}

func TestCheckRelaySnapshots_AgeAndDirectory_ReportsPrivacyRecovery(t *testing.T) {
	const name = "snap-20000101120000-0123456789abcdef.mrsnap"
	for _, state := range []string{"absent", "current", "old", "not directory"} {
		t.Run(state, func(t *testing.T) {
			dir, _ := initDiagnosticRelay(t, true)
			switch state {
			case "current":
				diagnosticSnapshot(t, dir, name, diagnosticCurrent())
			case "old":
				diagnosticSnapshot(t, dir, name, diagnosticOld())
			case "not directory":
				require.NoError(t, os.WriteFile(filepath.Join(dir, bootstrap.DirName), nil, 0o600))
			}
			before := time.Now().UTC()
			pass, detail := checkRelaySnapshots(dir)
			switch state {
			case "old":
				require.False(t, pass)
				require.Contains(t, detail, "1 stale bootstrap snapshot(s): "+name)
				ageBefore := int(before.Sub(diagnosticOld()).Hours() / 24)
				ageAfter := int(time.Now().UTC().Sub(diagnosticOld()).Hours() / 24)
				require.True(t, strings.Contains(detail, "("+strconv.Itoa(ageBefore)+"d)") ||
					strings.Contains(detail, "("+strconv.Itoa(ageAfter)+"d)"), "reported age must match the fixture mtime")
				require.Contains(t, detail, "full plaintext copy of the store")
				require.Contains(t, detail, "fix: delete it from the relay's bootstrap directory once no peer still needs it")
				require.FileExists(t, filepath.Join(dir, bootstrap.DirName, name))
			case "not directory":
				require.False(t, pass)
				require.Contains(t, detail, "read "+filepath.Join(dir, bootstrap.DirName))
			default:
				require.True(t, pass)
				require.Equal(t, "no stale bootstrap snapshots", detail)
			}
		})
	}
}

func TestCheckRelayPrivacy_Advisories_ExplainOperatorChoices(t *testing.T) {
	tests := []struct{ name, want string }{
		{"normal", "no privacy advisories"},
		{"cloud path", "move the relay to a volume you control, or accept it deliberately"},
		{"unauthenticated", "fix: re-init with authentication when the medium stops being fully trusted"},
		{"conflicted copies", "fix: remove them and check that each peer publishes under its own id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, doc := initDiagnosticRelay(t, tt.name != "unauthenticated")
			if tt.name == "cloud path" {
				dir = filepath.Join(dir, "OneDrive", "team")
				require.NoError(t, os.MkdirAll(dir, 0o700))
			}
			var artifacts []string
			if tt.name == "conflicted copies" {
				seg := diagnosticSegment(t, dir, diagnosticSelf, 1, diagnosticCurrent())
				for _, name := range []string{"seg-00000001 (1).mrseg", "seg-00000001 (conflicted copy).mrseg", "ordinary-notes.txt"} {
					path := filepath.Join(filepath.Dir(seg), name)
					require.NoError(t, os.WriteFile(path, nil, 0o600))
					if strings.Contains(name, "seg-") {
						artifacts = append(artifacts, filepath.Join(diagnosticSelf, name))
					}
				}
				require.Equal(t, artifacts, relayConflictedCopies(dir))
			}
			pass, detail := checkRelayPrivacy(dir, doc)
			require.Equal(t, tt.name == "normal", pass)
			require.Contains(t, detail, tt.want)
			if tt.name == "cloud path" {
				require.Contains(t, detail, "MAC'd but NOT encrypted")
				require.Contains(t, detail, "full event content reaches that provider")
			}
			if tt.name == "unauthenticated" {
				require.Contains(t, detail, "UNAUTHENTICATED")
			}
			if tt.name == "conflicted copies" {
				for _, artifact := range artifacts {
					require.Contains(t, detail, artifact)
					require.FileExists(t, filepath.Join(dir, tick.PeersDirName, filepath.Dir(artifact), tick.SegmentsDirName, filepath.Base(artifact)))
				}
				require.NotContains(t, detail, "ordinary-notes.txt")
			}
		})
	}
}
