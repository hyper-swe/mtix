// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// wantSettingsMessage is the fixed message for connection settings the
// driver cannot parse (FR-18.17, MTIX-95.15). The one parse runs in the
// TLS posture step, so New reports it under that step's prefix
// (MTIX-95.25).
const wantSettingsMessage = "tls posture: pgxpool parse: DSN could not be parsed: check the DSN's connection parameters"

// TestNew_UnparsableConnectionSettings_FixedMessage: when the driver
// cannot parse a DSN's connection settings, New reports one fixed
// message that quotes no part of the DSN, whether the password is in the
// user info or in a query setting (FR-18.17, MTIX-95.15).
func TestNew_UnparsableConnectionSettings_FixedMessage(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{"query password, duration timeout",
			"postgres://" + parseErrorUser + "@" + parseErrorHost + "/mtix?password=" + parseErrorSecret + "&connect_timeout=10s"},
		{"query password, word timeout",
			"postgres://" + parseErrorUser + "@" + parseErrorHost + "/mtix?password=" + parseErrorSecret + "&connect_timeout=bogus"},
		{"user-info password",
			"postgres://" + parseErrorUser + ":" + parseErrorSecret + "@" + parseErrorHost + "/mtix?connect_timeout=10s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			pool, err := transport.New(context.Background(), tt.dsn, transport.Options{})
			require.Error(t, err)
			require.Nil(t, pool)
			msg := err.Error()
			require.Equal(t, wantSettingsMessage, msg)
			require.NotContains(t, msg, parseErrorUser)
			require.NotContains(t, msg, parseErrorHost)
			for _, w := range secretWindows(parseErrorSecret) {
				require.NotContainsf(t, msg, w, "message repeats password fragment %q", w)
			}
		})
	}
}

// secretsTestDSN is the DSN the secrets-file tests write.
const secretsTestDSN = "postgres://" + parseErrorUser + ":" + parseErrorSecret + "@" + parseErrorHost + "/mtix"

// wantSecretsFileMessage is the fixed refusal for a secrets file that is
// not a regular file or exceeds 64 KiB (MTIX-95.15).
const wantSecretsFileMessage = "secrets file must be a regular file of at most 64 KiB"

// TestSource_SecretsFile_ReadAsRegularFileOfAtMost64KiB: Source follows a
// symlinked secrets file to its target, reads a regular file of at most
// 64 KiB, and refuses any other secrets file with a fixed message
// (FR-18.16, MTIX-95.15).
func TestSource_SecretsFile_ReadAsRegularFileOfAtMost64KiB(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")

	t.Run("symlink to a regular file is followed", func(t *testing.T) {
		dir := withMTIXDir(t)
		target := filepath.Join(t.TempDir(), "hub-dsn")
		require.NoError(t, os.WriteFile(target, []byte(secretsTestDSN+"\n"), 0o600))
		require.NoError(t, os.Symlink(target, filepath.Join(dir, transport.SecretsFilename)))
		got, err := transport.Source(dir)
		require.NoError(t, err)
		require.Equal(t, secretsTestDSN, got)
	})
	t.Run("exactly 64 KiB is read", func(t *testing.T) {
		dir := withMTIXDir(t)
		body := append([]byte(secretsTestDSN), bytes.Repeat([]byte(" "), 64<<10-len(secretsTestDSN))...)
		require.NoError(t, os.WriteFile(filepath.Join(dir, transport.SecretsFilename), body, 0o600))
		got, err := transport.Source(dir)
		require.NoError(t, err)
		require.Equal(t, secretsTestDSN, got)
	})
	t.Run("over 64 KiB is refused", func(t *testing.T) {
		dir := withMTIXDir(t)
		body := append([]byte(secretsTestDSN), bytes.Repeat([]byte(" "), 64<<10)...)
		require.NoError(t, os.WriteFile(filepath.Join(dir, transport.SecretsFilename), body, 0o600))
		got, err := transport.Source(dir)
		require.Error(t, err)
		require.Empty(t, got)
		require.Contains(t, err.Error(), wantSecretsFileMessage)
	})
	t.Run("a directory is refused", func(t *testing.T) {
		dir := withMTIXDir(t)
		require.NoError(t, os.Mkdir(filepath.Join(dir, transport.SecretsFilename), 0o600))
		got, err := transport.Source(dir)
		require.Error(t, err)
		require.Empty(t, got)
		require.Contains(t, err.Error(), wantSecretsFileMessage)
	})
}
