// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Synthetic DSN parts for the FR-18.17 output tests (MTIX-95.15). The
// secret mixes case and digits so none of its 3-character windows occurs
// in ordinary output; the host is under .invalid so no lookup succeeds.
const (
	sweepUser   = "sweepuser"
	sweepSecret = "Kq8vZ3xW9mRt"
	sweepHost   = "hub.invalid"
)

// DSN sources the output tests configure.
const (
	sourceEnv        = "env"
	sourceSecrets    = "secrets file"
	sourcePositional = "positional"
)

// syntheticDSN is one DSN form under test and the password it carries.
type syntheticDSN struct {
	name     string
	dsn      string
	password string
}

// wellFormedDSN is a DSN that parses and names an unreachable host.
func wellFormedDSN() syntheticDSN {
	return syntheticDSN{"well-formed",
		"postgres://" + sweepUser + ":" + sweepSecret + "@" + sweepHost + ":5432/mtix?connect_timeout=2",
		sweepSecret}
}

// malformedDSNs are DSNs that do not parse: bad URL syntax in the
// password, and no scheme at all.
func malformedDSNs() []syntheticDSN {
	return []syntheticDSN{
		{"malformed escape",
			"postgres://" + sweepUser + ":Kq8v%zzZ3xW9mRt@" + sweepHost + "/mtix", "Kq8v%zzZ3xW9mRt"},
		{"malformed userinfo",
			"postgres://" + sweepUser + ":Kq8v Z3xW9mRt@" + sweepHost + "/mtix", "Kq8v Z3xW9mRt"},
		{"malformed port",
			"postgres://" + sweepUser + ":Kq8v/Z3xW9mRt@" + sweepHost + "/mtix", "Kq8v/Z3xW9mRt"},
		{"scheme-less",
			sweepUser + ":" + sweepSecret + "@" + sweepHost + ":5432/mtix", sweepSecret},
	}
}

// queryPasswordDSN parses as a URL, carries its password as a query
// setting, and has a setting the driver rejects, so connecting fails
// with the driver's own error text.
func queryPasswordDSN() syntheticDSN {
	return syntheticDSN{"query password",
		"postgres://" + sweepUser + "@" + sweepHost + ":5432/mtix?password=" + sweepSecret + "&connect_timeout=bogus",
		sweepSecret}
}

// keywordDSN is a DSN in keyword/value form, which the hub transport
// does not accept.
func keywordDSN() syntheticDSN {
	return syntheticDSN{"keyword",
		"host=" + sweepHost + " user=" + sweepUser + " password=" + sweepSecret + " dbname=mtix",
		sweepSecret}
}

// syntheticDSNs returns every DSN form: well-formed, a query-setting
// password with a rejected setting, keyword/value, malformed and
// scheme-less.
func syntheticDSNs() []syntheticDSN {
	return append([]syntheticDSN{wellFormedDSN(), queryPasswordDSN(), keywordDSN()}, malformedDSNs()...)
}

// configureSyncDSN makes dsn this process's hub DSN through source and
// returns the positional arguments a command line would carry for it.
func configureSyncDSN(t *testing.T, mtixDir, source, dsn string) []string {
	t.Helper()
	switch source {
	case sourceEnv:
		t.Setenv(transport.EnvDSN, dsn)
		return nil
	case sourceSecrets:
		t.Setenv(transport.EnvDSN, "")
		require.NoError(t, os.WriteFile(filepath.Join(mtixDir, transport.SecretsFilename),
			[]byte(dsn+"\n"), transport.SecretsRequiredMode))
		return nil
	case sourcePositional:
		t.Setenv(transport.EnvDSN, "")
		return []string{dsn}
	default:
		t.Fatalf("unknown DSN source %q", source)
		return nil
	}
}

// requireNoSecret fails when text repeats the DSN, or its password or
// any 3-character window of the password.
func requireNoSecret(t *testing.T, label, text string, d syntheticDSN) {
	t.Helper()
	require.NotContainsf(t, text, d.dsn, "%s repeats the DSN:\n%s", label, text)
	for i := 0; i+3 <= len(d.password); i++ {
		w := d.password[i : i+3]
		require.NotContainsf(t, text, w, "%s repeats password fragment %q:\n%s", label, w, text)
	}
}

// requireNoDSNPart is requireNoSecret plus the user name and the host:
// for a DSN that does not parse, no part of it may appear.
func requireNoDSNPart(t *testing.T, label, text string, d syntheticDSN) {
	t.Helper()
	requireNoSecret(t, label, text, d)
	require.NotContainsf(t, text, sweepUser, "%s repeats the DSN user:\n%s", label, text)
	require.NotContainsf(t, text, sweepHost, "%s repeats the DSN host:\n%s", label, text)
}

// TestDoctorJSON_MalformedDSN_NoSecretInOutput: mtix sync doctor, in
// --json and text form, with a malformed or scheme-less DSN from the
// environment or the secrets file, names the parse failure without any
// part of the DSN (FR-18.17, MTIX-95.15). The output rule for doctor:
// no password and no DSN string ever, and nothing at all from a DSN
// that does not parse; for a DSN that parses, driver diagnostics may
// still name the user and host for troubleshooting.
func TestDoctorJSON_MalformedDSN_NoSecretInOutput(t *testing.T) {
	for _, d := range malformedDSNs() {
		for _, source := range []string{sourceEnv, sourceSecrets} {
			for _, jsonOut := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/json=%v", d.name, source, jsonOut), func(t *testing.T) {
					saveAndResetApp(t)
					app.mtixDir = t.TempDir()
					app.jsonOutput = jsonOut
					args := configureSyncDSN(t, app.mtixDir, source, d.dsn)

					var stdout, stderr bytes.Buffer
					err := runSyncDoctor(context.Background(), &stdout, &stderr,
						args, transport.Options{})
					require.ErrorIs(t, err, errDoctorChecksFailed)

					out := stdout.String() + stderr.String()
					requireNoDSNPart(t, "doctor output", out, d)
					require.Contains(t, out, "DSN could not be parsed",
						"the PG check names the parse failure")
					if jsonOut {
						var report DoctorReport
						require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
						require.NotEmpty(t, report.Checks)
						for _, c := range report.Checks {
							requireNoDSNPart(t, "check "+c.Name, c.Detail, d)
						}
					}
				})
			}
		}
	}
}

// TestDoctor_DriverErrorText_NoSecretInOutput: when connecting fails
// with the driver's own error text, mtix sync doctor reports that text
// without the password, in text and --json form; the driver's text may
// name the user and host (FR-18.17, MTIX-95.15).
func TestDoctor_DriverErrorText_NoSecretInOutput(t *testing.T) {
	d := queryPasswordDSN()
	for _, jsonOut := range []bool{true, false} {
		t.Run(fmt.Sprintf("json=%v", jsonOut), func(t *testing.T) {
			saveAndResetApp(t)
			app.mtixDir = t.TempDir()
			app.jsonOutput = jsonOut
			configureSyncDSN(t, app.mtixDir, sourceEnv, d.dsn)

			var stdout, stderr bytes.Buffer
			err := runSyncDoctor(context.Background(), &stdout, &stderr, nil, transport.Options{})
			require.ErrorIs(t, err, errDoctorChecksFailed)
			out := stdout.String() + stderr.String()
			require.Contains(t, out, "invalid connect_timeout", "the driver's error is still reported")
			requireNoSecret(t, "doctor output", out, d)
		})
	}
}

// TestWrapSyncErr_KnownDSN_ScrubbedEvenWhenUnparseable: the central
// scrubber behind wrapSyncErr removes the exact configured DSN and its
// password, whether or not the DSN parses, plus any URL-shaped DSN, on
// both the returned error and the hook-mode warning (FR-18.17,
// MTIX-95.15).
func TestWrapSyncErr_KnownDSN_ScrubbedEvenWhenUnparseable(t *testing.T) {
	const otherPassword = "OtherPw9x"
	for _, d := range syntheticDSNs() {
		for _, source := range []string{sourceEnv, sourceSecrets} {
			for _, hook := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/hook=%v", d.name, source, hook), func(t *testing.T) {
					saveAndResetApp(t)
					app.mtixDir = t.TempDir()
					configureSyncDSN(t, app.mtixDir, source, d.dsn)
					hookEnv := ""
					if hook {
						hookEnv = "1"
					}
					t.Setenv("MTIX_SYNC_HOOK", hookEnv)

					// Each secret appears twice: every occurrence must go.
					cause := fmt.Errorf("connection refused: dial [%s] password %s; "+
						"retry [%s] password %s; also postgres://other:%s@elsewhere.invalid/db",
						d.dsn, d.password, d.dsn, d.password, otherPassword)
					var stderr bytes.Buffer
					err := wrapSyncErr(&stderr, "connect", cause)
					text := stderr.String()
					if hook {
						require.NoError(t, err, "hook mode degrades a transient error to a warning")
						require.Contains(t, text, "WARN: mtix sync connect degraded")
					} else {
						require.Error(t, err)
						text += err.Error()
						require.Contains(t, text, "mtix sync connect: connection refused")
					}
					requireNoSecret(t, "wrapped error", text, d)
					require.NotContains(t, text, otherPassword, "URL-shaped DSNs are masked too")
				})
			}
		}
	}
}

// TestPgDumpConnParams_MalformedDSN_FixedMessage: mtix sync backup's own
// DSN parse reports one fixed message and no part of the DSN (FR-18.17,
// MTIX-95.15).
func TestPgDumpConnParams_MalformedDSN_FixedMessage(t *testing.T) {
	for _, d := range malformedDSNs() {
		t.Run(d.name, func(t *testing.T) {
			_, err := pgDumpConnParams(d.dsn)
			require.Error(t, err)
			require.Equal(t, "parse backup dsn: DSN could not be parsed", err.Error())
			requireNoDSNPart(t, "backup parse error", err.Error(), d)
		})
	}
}

// TestSyncResolveCommands_NonIntegerID_NotEchoed: a DSN typed where a
// conflict or collision id belongs is refused without being repeated
// (FR-18.17, MTIX-95.15).
func TestSyncResolveCommands_NonIntegerID_NotEchoed(t *testing.T) {
	for _, d := range syntheticDSNs() {
		t.Run(d.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runSyncConflictsResolve(context.Background(), &stdout, &stderr,
				d.dsn, "acknowledge")
			require.Error(t, err)
			require.Contains(t, err.Error(), "conflict_id must be an integer")
			requireNoSecret(t, "conflicts resolve", err.Error()+stdout.String()+stderr.String(), d)

			stdout.Reset()
			stderr.Reset()
			err = runSyncCollisionsResolve(context.Background(), &stdout, &stderr,
				[]string{d.dsn}, transport.Options{}, collisionWinnerHeld)
			require.Error(t, err)
			require.Contains(t, err.Error(), "collision_id must be an integer")
			requireNoSecret(t, "collisions resolve", err.Error()+stdout.String()+stderr.String(), d)
		})
	}
}

// errorsAsText joins an error and captured output for a leak check.
func errorsAsText(err error, outputs ...string) string {
	var b bytes.Buffer
	if err != nil {
		b.WriteString(err.Error())
		b.WriteByte('\n')
	}
	for _, o := range outputs {
		b.WriteString(o)
	}
	return b.String()
}

// TestScrubSyncText_EnvAndSecretsFile_BothScrubbed: the central scrubber
// removes the MTIX_SYNC_DSN value and the .mtix/secrets content alike,
// whichever one resolution would pick and whatever the file's mode
// (FR-18.17, MTIX-95.15).
func TestScrubSyncText_EnvAndSecretsFile_BothScrubbed(t *testing.T) {
	saveAndResetApp(t)
	app.mtixDir = t.TempDir()
	envDSN := wellFormedDSN()
	fileDSN := syntheticDSN{"unparseable, second secret",
		"postgres://" + sweepUser + ":Wq5n%zzL7pY2cHs@" + sweepHost + "/mtix", "Wq5n%zzL7pY2cHs"}
	t.Setenv(transport.EnvDSN, envDSN.dsn)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, transport.SecretsFilename),
		[]byte(fileDSN.dsn+"\n"), 0o644))

	got := scrubSyncText("env " + envDSN.password + " file " + fileDSN.password + " end")
	requireNoSecret(t, "scrubbed text", got, envDSN)
	requireNoSecret(t, "scrubbed text", got, fileDSN)
	require.Equal(t, "env REDACTED file REDACTED end", got)

	// Outside any project only the environment value is known, even when
	// the working directory holds a file named like the secrets file.
	t.Chdir(app.mtixDir)
	app.mtixDir = ""
	require.Equal(t, "env REDACTED file "+fileDSN.password,
		scrubSyncText("env "+envDSN.password+" file "+fileDSN.password),
		"outside a project only the environment value is known")
}

// newProjectWithSecrets makes a project directory whose .mtix/secrets
// holds dsn (mode 0600) and returns the project and .mtix paths.
func newProjectWithSecrets(t *testing.T, dsn string) (string, string) {
	t.Helper()
	project := t.TempDir()
	mtixDir := filepath.Join(project, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(project, "sub", "dir"), 0o755))
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, transport.SecretsFilename),
		[]byte(dsn+"\n"), transport.SecretsRequiredMode))
	return project, mtixDir
}

// TestScrubSyncText_BeforeAppInit_FindsProjectSecrets: before the app is
// initialized (flag and argument errors), the scrubber finds the
// project's .mtix/secrets by walking up from the working directory
// (FR-18.17, MTIX-95.15).
func TestScrubSyncText_BeforeAppInit_FindsProjectSecrets(t *testing.T) {
	saveAndResetApp(t)
	t.Setenv(transport.EnvDSN, "")
	d := malformedDSNs()[3] // scheme-less: no URL shape to mask
	project, _ := newProjectWithSecrets(t, d.dsn)
	t.Chdir(filepath.Join(project, "sub", "dir"))

	got := scrubSyncText("bad value " + d.dsn + " and " + d.password)
	requireNoSecret(t, "scrubbed text", got, d)
	require.Equal(t, "bad value REDACTED and REDACTED", got)
}

// TestScrubSyncText_SecretsFileNotRegularOrTooLarge_NotRead: the scrubber
// reads .mtix/secrets only when it is a regular file of at most 64 KiB;
// a symlink or an oversized file contributes nothing (MTIX-95.15).
func TestScrubSyncText_SecretsFileNotRegularOrTooLarge_NotRead(t *testing.T) {
	saveAndResetApp(t)
	t.Setenv(transport.EnvDSN, "")
	d := wellFormedDSN()
	_, mtixDir := newProjectWithSecrets(t, d.dsn)
	app.mtixDir = mtixDir
	secrets := filepath.Join(mtixDir, transport.SecretsFilename)
	text := "value " + d.password
	require.Equal(t, "value REDACTED", scrubSyncText(text), "a regular file is read")

	target := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.Rename(secrets, target))
	require.NoError(t, os.Symlink(target, secrets))
	require.Equal(t, text, scrubSyncText(text), "a symlink is not followed")

	require.NoError(t, os.Remove(secrets))
	big := append([]byte(d.dsn+"\n"), bytes.Repeat([]byte("#"), 64<<10)...)
	require.NoError(t, os.WriteFile(secrets, big, transport.SecretsRequiredMode))
	require.Equal(t, text, scrubSyncText(text), "a file over 64 KiB is not read")

	require.NoError(t, os.WriteFile(secrets, big[:64<<10], transport.SecretsRequiredMode))
	require.Equal(t, "value REDACTED", scrubSyncText(text), "a file of exactly 64 KiB is read")
}

// TestPrintFinalError_FlagErrorBeforeAppInit_SecretsFileDSNScrubbed: a
// flag value that repeats the secrets-file DSN fails before the app is
// initialized, and the final error line still prints no secret
// (FR-18.17, MTIX-95.15).
func TestPrintFinalError_FlagErrorBeforeAppInit_SecretsFileDSNScrubbed(t *testing.T) {
	saveAndResetApp(t)
	t.Setenv(transport.EnvDSN, "")
	d := malformedDSNs()[3] // scheme-less: no URL shape to mask
	project, _ := newProjectWithSecrets(t, d.dsn)
	t.Chdir(project)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"daemon", "--interval", d.dsn})
	err := root.Execute()
	require.Error(t, err, "the flag value is not an integer")
	require.Empty(t, app.mtixDir, "the failure happens before app init")

	var buf bytes.Buffer
	printFinalError(&buf, err)
	require.True(t, strings.HasPrefix(buf.String(), "error: "), buf.String())
	requireNoSecret(t, "final error", buf.String()+out.String(), d)
}

// TestPrintFinalError_DSNInError_Scrubbed: the CLI's final error line
// passes through the central scrubber, so an error cobra builds around
// an argument, or any error quoting the configured DSN, prints no
// secret (FR-18.17, MTIX-95.15).
func TestPrintFinalError_DSNInError_Scrubbed(t *testing.T) {
	saveAndResetApp(t)
	app.mtixDir = t.TempDir()
	known := malformedDSNs()[3] // scheme-less: no URL shape to mask
	t.Setenv(transport.EnvDSN, known.dsn)
	other := wellFormedDSN()

	var buf bytes.Buffer
	printFinalError(&buf, fmt.Errorf("unknown command %q for %q; hub %s", other.dsn, "mtix", known.dsn))
	got := buf.String()
	require.True(t, strings.HasPrefix(got, "error: unknown command "), got)
	require.True(t, strings.HasSuffix(got, "\n"), "one line")
	requireNoSecret(t, "final error", got, other)
	requireNoSecret(t, "final error", got, known)
}

// TestSyncBackup_PgDumpStderr_NoSecret: pg_dump's own stderr reaches the
// terminal through the central scrubber, so a pg_dump message that
// quotes the connection password prints without it (FR-18.17,
// MTIX-95.15).
func TestSyncBackup_PgDumpStderr_NoSecret(t *testing.T) {
	initTestApp(t)
	d := wellFormedDSN()
	t.Setenv(transport.EnvDSN, d.dsn)
	t.Setenv("MTIX_SYNC_HOOK", "")
	fake := filepath.Join(t.TempDir(), "pg_dump")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\n"+
		"printf 'pg_dump: error: connection failed for %s\\n' \"$PGPASSWORD\" >&2\n"+
		"printf 'pg_dump: detail: %s (no newline)' \"$PGPASSWORD\" >&2\n"+
		"exit 1\n"), 0o700)) //nolint:gosec // test stub must be executable
	t.Setenv("MTIX_PG_DUMP", fake)

	stdout, stderr, err := execSyncCLI(context.Background(),
		[]string{"sync", "backup", "--output", filepath.Join(t.TempDir(), "hub.sql")})
	require.Error(t, err)
	require.Contains(t, stderr, "pg_dump: error: connection failed for REDACTED\n",
		"pg_dump's message is still shown, without the password")
	require.Contains(t, stderr, "pg_dump: detail: REDACTED (no newline)",
		"a final line without a newline is flushed, scrubbed")
	requireNoSecret(t, "sync backup", errorsAsText(err, stdout, stderr), d)
}

// failingWriter fails every write, to exercise scrubWriter's error path.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

// TestScrubWriter_LinesScrubbedAcrossWrites: the scrubbing writer that
// carries pg_dump's stderr removes a known password even when it is
// split across writes, writes each line once, flushes a final line
// without a newline, and reports a failed underlying write (FR-18.17,
// MTIX-95.15).
func TestScrubWriter_LinesScrubbedAcrossWrites(t *testing.T) {
	saveAndResetApp(t)
	app.mtixDir = t.TempDir()
	d := wellFormedDSN()
	t.Setenv(transport.EnvDSN, d.dsn)

	var out bytes.Buffer
	w := newScrubWriter(&out)
	for _, chunk := range []string{"first Kq8v", "Z3xW9mRt line\nsecond ", "line\nlast ", d.password} {
		n, err := w.Write([]byte(chunk))
		require.NoError(t, err)
		require.Equal(t, len(chunk), n)
	}
	require.Equal(t, "first REDACTED line\nsecond line\n", out.String(),
		"complete lines are written, the partial last line is held")
	require.NoError(t, w.Flush())
	require.Equal(t, "first REDACTED line\nsecond line\nlast REDACTED", out.String())
	require.NoError(t, w.Flush(), "flushing with nothing held writes nothing")
	require.Equal(t, "first REDACTED line\nsecond line\nlast REDACTED", out.String())

	failing := newScrubWriter(failingWriter{})
	n, err := failing.Write([]byte("a line\n"))
	require.ErrorIs(t, err, os.ErrClosed)
	require.Zero(t, n)
	_, err = failing.Write([]byte("partial"))
	require.NoError(t, err, "a partial line is only held")
	require.ErrorIs(t, failing.Flush(), os.ErrClosed)
	require.NoError(t, newScrubWriter(failingWriter{}).Flush(), "nothing held, nothing written")
}
