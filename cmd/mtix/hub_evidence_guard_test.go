// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-107.31.8: one helper gathers the hub evidence for every replace guard.
// An exported DSN is transient evidence, evidence that cannot be read is an
// error, and a missing secrets file is no evidence.

func TestCollectHubEvidence_ExportedDSN_IsTransientEvidenceOnly(t *testing.T) {
	t.Setenv(transport.EnvDSN, "postgres://hub.invalid/mtix")
	initTestApp(t)

	ev, err := collectHubEvidence()
	require.NoError(t, err)
	assert.True(t, ev.EnvDSNExported, "an exported DSN is seen")
	assert.False(t, ev.EnvDSNConnected, "but nothing connected with it")
	assert.False(t, ev.SecretsDSN)
	assert.False(t, ev.ConfigKeys)
}

func TestCollectHubEvidence_SecretsFile_Cases(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, mtixDir string)
		wantDSN bool
		wantErr bool
	}{
		{"no secrets file", func(*testing.T, string) {}, false, false},
		{"a DSN in the secrets file", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, transport.SecretsFilename), []byte("postgres://hub.invalid/mtix\n"), 0o600))
		}, true, false},
		{"an empty secrets file", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, transport.SecretsFilename), []byte("  \n"), 0o600))
		}, false, false},
		{"a secrets path that is not a regular file", func(t *testing.T, dir string) {
			require.NoError(t, os.Mkdir(filepath.Join(dir, transport.SecretsFilename), 0o700))
		}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, "")
			initTestApp(t)
			tt.setup(t, app.mtixDir)

			ev, err := collectHubEvidence()
			if tt.wantErr {
				require.ErrorIs(t, err, transport.ErrSecretsFileInvalid)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantDSN, ev.SecretsDSN)
		})
	}
}

// The guards fail closed: evidence that cannot be read counts as a hub, with
// the cause, and an exported DSN counts while it is set and no longer after.
func TestHubGuardCounts_Cases(t *testing.T) {
	tests := []struct {
		name      string
		dsn       string
		setup     func(t *testing.T, mtixDir string)
		want      bool
		wantErr   bool
		wantCause error
	}{
		{"nothing configured", "", func(*testing.T, string) {}, false, false, nil},
		{"an exported DSN", "postgres://hub.invalid/mtix", func(*testing.T, string) {}, true, false, nil},
		{"unreadable secrets file", "", func(t *testing.T, dir string) {
			require.NoError(t, os.Mkdir(filepath.Join(dir, transport.SecretsFilename), 0o700))
		}, true, true, transport.ErrSecretsFileInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, tt.dsn)
			initTestApp(t)
			tt.setup(t, app.mtixDir)

			got, err := hubGuardCounts(context.Background())
			assert.Equal(t, tt.want, got)
			if tt.wantErr {
				require.ErrorIs(t, err, tt.wantCause)
				return
			}
			require.NoError(t, err)
		})
	}
}

// refuseSyncedReplace and the doctor check refuse when the evidence cannot be
// read, naming the cause.
func TestReplaceGuards_UnreadableEvidence_FailClosed(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	initTestApp(t)
	seedTickets(t, 2)
	require.NoError(t, os.Mkdir(filepath.Join(app.mtixDir, transport.SecretsFilename), 0o700))

	err := refuseSyncedReplace(context.Background())
	require.ErrorIs(t, err, sqlite.ErrReplaceOnSyncedStore)
	require.ErrorIs(t, err, transport.ErrSecretsFileInvalid)

	r := appendLoggedNodesCheck(context.Background(), DoctorReport{OverallPass: true}, app.store)
	require.Len(t, r.Checks, 1)
	assert.False(t, r.Checks[0].Pass)
	assert.Contains(t, r.Checks[0].Detail, "read the hub evidence")
}

// A secrets path that holds something unreadable is evidence that cannot be
// read: a dangling link, a file nobody may read, and one past the size cap
// all fail closed (MTIX-107.31.8). Only a path that is not there is none.
func TestCollectHubEvidence_UnreadableSecrets_FailClosed(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, secrets string)
	}{
		{"a dangling link", func(t *testing.T, secrets string) {
			require.NoError(t, os.Symlink(filepath.Join(filepath.Dir(secrets), "missing-target"), secrets))
		}},
		{"a file nobody may read", func(t *testing.T, secrets string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads any file")
			}
			require.NoError(t, os.WriteFile(secrets, []byte("postgres://hub.invalid/mtix\n"), 0o000))
		}},
		{"a file past the size cap", func(t *testing.T, secrets string) {
			big := make([]byte, transport.MaxSecretsFileSize+1)
			require.NoError(t, os.WriteFile(secrets, big, 0o600))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, "")
			initTestApp(t)
			tt.setup(t, filepath.Join(app.mtixDir, transport.SecretsFilename))

			_, err := collectHubEvidence()
			require.Error(t, err, "evidence that cannot be read is not 'none'")
			counts, guardErr := hubGuardCounts(context.Background())
			assert.True(t, counts)
			require.Error(t, guardErr)
		})
	}
}

// A DSN that transport.Source refuses (a DSN key in a tracked config file, or a
// secrets file with the wrong mode) still counts for the replace guards, as it
// always did, but only transiently: it refuses the CLI replace and the
// doctor check runs, and it never writes the sticky mark. The mark follows a
// DSN in the secrets file by its content, whatever the mode, as before.
func TestCollectHubEvidence_RefusedDSN_CountsTransientlyAndMarksOnlyByContent(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(t *testing.T, mtixDir string)
		wantMark     bool
		wantRefusing bool
	}{
		{"a DSN key in a tracked config file", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("sync.dsn: x\n"), 0o644))
		}, false, true},
		{"a DSN in a secrets file with the wrong mode", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, transport.SecretsFilename), []byte("postgres://hub.invalid/mtix\n"), 0o644))
		}, true, true},
		{"an empty secrets file with the wrong mode", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, transport.SecretsFilename), nil, 0o644))
		}, false, true},
		{"nothing", func(*testing.T, string) {}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, "")
			initTestApp(t)
			seedTickets(t, 2)
			tt.setup(t, app.mtixDir)
			ctx := context.Background()

			ev, err := collectHubEvidence()
			require.NoError(t, err)
			assert.Equal(t, tt.wantRefusing, ev.DSNRefused)
			_, err = service.NewHubMarkService(app.store).HubConnected(ctx, ev)
			require.NoError(t, err)
			mark, err := app.store.HubMark(ctx)
			require.NoError(t, err)
			assert.Equal(t, tt.wantMark, mark.Set, "the sticky mark follows a DSN's content only")

			counts, err := hubGuardCounts(ctx)
			require.NoError(t, err)
			assert.Equal(t, tt.wantRefusing, counts, "the guards count it")
			replaceErr := refuseSyncedReplace(ctx)
			if tt.wantRefusing {
				require.ErrorIs(t, replaceErr, sqlite.ErrReplaceOnSyncedStore)
			} else {
				require.NoError(t, replaceErr)
			}
			r := appendLoggedNodesCheck(ctx, DoctorReport{OverallPass: true}, app.store)
			require.Len(t, r.Checks, 1)
			assert.Equal(t, !tt.wantRefusing, strings.Contains(r.Checks[0].Detail, "does not sync"),
				"the doctor check runs when a DSN is refused")
		})
	}
}

// A sync command that has just connected records the mark even when the hub
// evidence cannot be read: unreadable evidence never lowers the record of a
// proven connection.
func TestNoteHubConnection_UnreadableEvidence_StillRecordsTheConnection(t *testing.T) {
	t.Setenv(transport.EnvDSN, "postgres://hub.invalid/mtix")
	initTestApp(t)
	require.NoError(t, os.Mkdir(filepath.Join(app.mtixDir, transport.SecretsFilename), 0o700))
	var stderr bytes.Buffer

	noteHubConnection(context.Background(), &stderr)

	mark, err := app.store.HubMark(context.Background())
	require.NoError(t, err)
	assert.True(t, mark.Set, "the connection is recorded")
	assert.Contains(t, stderr.String(), "could not read the hub evidence", "and the warning is printed")
}

// A stored mark counts for the identity rule even when the evidence cannot be
// read; with no mark, unreadable evidence is not a connection.
func TestHubMarkConnected_UnreadableEvidence_FollowsTheStoredMark(t *testing.T) {
	tests := []struct {
		name   string
		marked bool
	}{
		{"a stored mark", true},
		{"no stored mark", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, "")
			initTestApp(t)
			if tt.marked {
				flipped, err := service.NewHubMarkService(app.store).RecordEnvConnection(context.Background())
				require.NoError(t, err)
				require.True(t, flipped)
			}
			require.NoError(t, os.Mkdir(filepath.Join(app.mtixDir, transport.SecretsFilename), 0o700))

			assert.Equal(t, tt.marked, hubMarkConnected())
			assert.Equal(t, tt.marked, identityBoardConnected(oplocal.BoardObservation{}))
		})
	}
}

// Unreadable evidence keeps what was read before it: a config key that says a
// hub is set up still counts, and still names config as the source of the
// recorded mark (as before this change).
func TestUnreadableSecrets_KeepsTheConfigEvidenceAlreadyRead(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	initTestApp(t)
	_, err := app.configSvc.Set("sync.enabled", "true")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(app.mtixDir, transport.SecretsFilename), 0o700))

	assert.True(t, hubMarkConnected(), "sync.enabled counts although the secrets file cannot be read")
	assert.True(t, lenientHubEvidence().ConfigKeys)
	var stderr bytes.Buffer
	noteHubConnection(context.Background(), &stderr)
	mark, err := app.store.HubMark(context.Background())
	require.NoError(t, err)
	assert.True(t, mark.Set)
	assert.Equal(t, "config", mark.Source, "the recorded source stays config")
}

// board.hub-mark fails, with the cause, when the evidence cannot be read.
func TestHubMarkCheck_UnreadableEvidence_FailsWithTheCause(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	initTestApp(t)
	require.NoError(t, os.Mkdir(filepath.Join(app.mtixDir, transport.SecretsFilename), 0o700))

	c := hubMarkCheck(t)
	assert.False(t, c.Pass)
	assert.Contains(t, c.Detail, "cannot read the hub evidence")
	assert.Contains(t, c.Detail, "secrets")
}

// board.hub-mark fails when the evidence cannot be read, and still records the
// durable part it did read, as before this change.
func TestHubMarkCheck_PartialEvidence_FailsAndRecordsTheConfigMark(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	initTestApp(t)
	_, err := app.configSvc.Set("sync.enabled", "true")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(app.mtixDir, transport.SecretsFilename), 0o700))

	c := hubMarkCheck(t)
	assert.False(t, c.Pass)
	assert.Contains(t, c.Detail, "cannot read the hub evidence")
	mark, markErr := app.store.HubMark(context.Background())
	require.NoError(t, markErr)
	assert.True(t, mark.Set)
	assert.Equal(t, "config", mark.Source)
}
