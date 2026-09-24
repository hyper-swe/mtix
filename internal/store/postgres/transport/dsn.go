// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package transport is the PG client wrapper for the sync hub per
// FR-18.3 / SYNC-DESIGN section 5. This file owns DSN sourcing and TLS
// posture; pool.go owns the pgxpool wrapper; migrate.go owns the
// schema migration runner.
//
// All exported functions return wrapped errors that pass through
// RedactDSN before any callsite logs them — see internal/sync/redact
// (lands in MTIX-15.3.4). Until that ships, callers MUST NOT log the
// raw error string from this package.
package transport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hyper-swe/mtix/internal/model"
)

// EnvDSN is the canonical environment variable for the hub DSN per
// FR-18.16. Setting this is the production-ready path; the secrets
// file is the development convenience.
const EnvDSN = "MTIX_SYNC_DSN"

// EnvSSLRootCert names the env var holding a path to the TLS CA bundle
// per FR-18.15. Managed PG providers (Supabase, Neon, RDS) commonly
// require this; the workflow docs in MTIX-15.12 show how to fetch it.
const EnvSSLRootCert = "MTIX_SYNC_SSLROOTCERT"

// SecretsFilename is the per-project credential file. Mode 0600
// enforced; gitignore rule auto-installed by mtix sync init in
// MTIX-15.7.
const SecretsFilename = "secrets"

// SecretsRequiredMode is the FR-18.16 mode requirement.
const SecretsRequiredMode os.FileMode = 0o600

// MaxSecretsFileSize is the largest secrets file ReadSecretsFile reads
// (MTIX-95.15).
const MaxSecretsFileSize = 64 << 10

// trackedConfigCandidates lists the file paths under .mtix/ that
// MUST NOT contain a DSN. If a tracked YAML/JSON config holds a key
// suggesting a DSN was committed, Source returns ErrDSNInTrackedFile.
var trackedConfigCandidates = []string{"config.yaml", "config.yml", "config.json"}

// trackedDSNKeys are the keys whose presence in a tracked config file
// triggers ErrDSNInTrackedFile. Heuristic only; the real defense is
// the secret-file mode requirement.
var trackedDSNKeys = []string{"sync.dsn", "pg.dsn", "postgres.dsn", "MTIX_SYNC_DSN"}

// Sentinel errors so callers can errors.Is to dispatch.
var (
	// ErrDSNNotConfigured is returned when neither the env var nor the
	// secrets file provides a DSN.
	ErrDSNNotConfigured = errors.New("no DSN configured: set MTIX_SYNC_DSN or .mtix/secrets")

	// ErrSecretsFileMode is returned when .mtix/secrets exists with a
	// looser-than-0600 permission mode.
	ErrSecretsFileMode = errors.New("secrets file mode too permissive")

	// ErrDSNInTrackedFile is returned when a tracked config file under
	// .mtix/ appears to contain a DSN.
	ErrDSNInTrackedFile = errors.New("DSN found in tracked config file (FR-18.16 forbidden)")

	// ErrTLSWeakNonLoopback is returned when --insecure-tls is requested
	// but a host the connection may use is not loopback or a local
	// Unix-domain socket, or the hosts cannot be resolved.
	ErrTLSWeakNonLoopback = errors.New("weak TLS only allowed on loopback hosts or local sockets")

	// ErrTLSWeakWithoutFlag is returned when the parsed DSN has a weak
	// sslmode but --insecure-tls was not set.
	ErrTLSWeakWithoutFlag = errors.New("weak sslmode requires --insecure-tls")

	// ErrDSNMalformed is wrapped by every error for a DSN that cannot be
	// parsed. The messages that wrap it are fixed and quote no part of
	// the DSN (FR-18.17, MTIX-95.15).
	ErrDSNMalformed = errors.New("DSN could not be parsed")

	// ErrPositionalDSN is returned for a positional argument where none
	// is accepted, which includes a DSN given on the command line. The
	// hub DSN comes only from MTIX_SYNC_DSN or .mtix/secrets; the message
	// is fixed and never repeats the argument (FR-18.16, MTIX-95.15).
	ErrPositionalDSN = errors.New("unexpected argument; a hub DSN is not accepted on the command line: " +
		"set MTIX_SYNC_DSN or .mtix/secrets")

	// ErrSecretsFileInvalid is returned when the secrets file, after
	// following a symlink, is not a regular file or is larger than
	// MaxSecretsFileSize (MTIX-95.15).
	ErrSecretsFileInvalid = errors.New("secrets file must be a regular file of at most 64 KiB")
)

// Options control non-DSN behavior of the transport.
type Options struct {
	// InsecureTLS allows sslmode weaker than verify-full ONLY when every
	// host the connection may use is loopback or a local Unix-domain
	// socket (FR-18.15). Default false.
	InsecureTLS bool
}

// Source resolves the hub DSN from the FR-18.16 sources, in order:
//
//  1. The MTIX_SYNC_DSN environment variable.
//  2. The mtixDir/secrets file (mode 0600, gitignored).
//
// Refuses to load from any tracked config file under mtixDir even if
// the env var is also set — fail closed at the earliest detectable
// misconfiguration.
//
// Returns ErrDSNNotConfigured if no source provides a value.
func Source(mtixDir string) (string, error) {
	if mtixDir == "" {
		return "", fmt.Errorf("source DSN: mtixDir required: %w", model.ErrInvalidInput)
	}

	// Step 1: refuse to proceed if a tracked config file even mentions
	// a DSN key. This is fail-closed; the env var path is irrelevant
	// once we suspect a leak.
	if err := refuseDSNInTrackedConfig(mtixDir); err != nil {
		return "", err
	}

	// Step 2: env var.
	if v := os.Getenv(EnvDSN); v != "" {
		return v, nil
	}

	// Step 3: secrets file, read by the same reader the output scrubber
	// uses, so the two always agree on the DSN (MTIX-95.15).
	secretsPath := filepath.Join(mtixDir, SecretsFilename)
	body, mode, err := ReadSecretsFile(mtixDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrDSNNotConfigured
		}
		return "", err
	}
	if mode != SecretsRequiredMode {
		return "", fmt.Errorf("%s: %w (want 0600, got %#o)",
			secretsPath, ErrSecretsFileMode, mode)
	}
	dsn := strings.TrimSpace(body)
	if dsn == "" {
		return "", ErrDSNNotConfigured
	}
	return dsn, nil
}

// ReadSecretsFile reads mtixDir's secrets file and returns its content
// and permission bits (FR-18.16, MTIX-95.15). A symlink is followed; the
// target must be a regular file of at most MaxSecretsFileSize bytes,
// else the error wraps ErrSecretsFileInvalid. An absent file gives an
// error wrapping os.ErrNotExist. Source and the output scrubber both
// read the file through this function, so they always agree on it.
func ReadSecretsFile(mtixDir string) (string, os.FileMode, error) {
	path := filepath.Join(mtixDir, SecretsFilename)
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s: %w", path, ErrSecretsFileInvalid)
	}
	f, err := os.Open(path) //nolint:gosec // path is mtixDir + the fixed secrets filename
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, MaxSecretsFileSize+1))
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", path, err)
	}
	if len(body) > MaxSecretsFileSize {
		return "", 0, fmt.Errorf("%s: %w", path, ErrSecretsFileInvalid)
	}
	return string(body), info.Mode().Perm(), nil
}

// refuseDSNInTrackedConfig scans .mtix/config.{yaml,yml,json} for any
// of the trackedDSNKeys. If found, returns ErrDSNInTrackedFile so the
// caller can surface a structured error and refuse to proceed.
//
// This is a best-effort scanner; the real defense is the file-mode
// requirement on .mtix/secrets and gitignore on .mtix/secrets.
func refuseDSNInTrackedConfig(mtixDir string) error {
	for _, name := range trackedConfigCandidates {
		p := filepath.Join(mtixDir, name)
		body, err := os.ReadFile(p) //nolint:gosec // p is mtixDir + canonical filename
		if err != nil {
			continue
		}
		text := string(body)
		for _, key := range trackedDSNKeys {
			if strings.Contains(text, key) {
				return fmt.Errorf("%s mentions %q: %w", p, key, ErrDSNInTrackedFile)
			}
		}
	}
	return nil
}

// EnforceTLSPosture parses the DSN, defaults sslmode to verify-full
// when omitted, and refuses weaker sslmodes unless opts.InsecureTLS is
// set AND every host the connection may use is local (FR-18.15,
// MTIX-95.20).
//
// The hosts are resolved by the driver's own parser, pgconn.ParseConfig,
// so the primary host and every fallback count, whether they come from
// the DSN host list, its connection parameters, PG* environment
// variables or a service file. A host is local when it is loopback
// (see isLoopback) or a Unix-domain socket directory, which the driver
// never wraps in TLS. A DSN whose hosts cannot be resolved is refused.
// No refusal quotes host text: a refused host is named by its position
// in the resolved list.
//
// Returns the (possibly modified) DSN with sslmode populated and
// MTIX_SYNC_SSLROOTCERT honored. The returned DSN is ready for
// pgxpool.New.
func EnforceTLSPosture(dsn string, opts Options) (string, error) {
	parsed, err := parseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}

	q := parsed.Query()
	mode := strings.ToLower(q.Get("sslmode"))
	if mode == "" {
		mode = "verify-full"
		q.Set("sslmode", mode)
	}
	if mode != "verify-full" {
		if !opts.InsecureTLS {
			return "", fmt.Errorf("sslmode=%s: %w", mode, ErrTLSWeakWithoutFlag)
		}
		hosts, resolveErr := resolveHosts(parsed, q)
		if resolveErr != nil {
			return "", fmt.Errorf("sslmode=%s: %w", mode, resolveErr)
		}
		for i, h := range hosts {
			if network, _ := pgconn.NetworkAddress(h.Host, h.Port); network != "unix" && !isLoopback(h.Host) {
				return "", fmt.Errorf("sslmode=%s: host %d of %d is not loopback or a local socket: %w",
					mode, i+1, len(hosts), ErrTLSWeakNonLoopback)
			}
		}
	}

	if rootCert := os.Getenv(EnvSSLRootCert); rootCert != "" && q.Get("sslrootcert") == "" {
		q.Set("sslrootcert", rootCert)
	}

	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

// parseDSN parses a postgres:// or postgresql:// URL form and rejects
// anything else (FR-18.15). Every failure is a fixed message wrapping
// ErrDSNMalformed that quotes no part of the DSN; the parser's own
// error is discarded, since it may quote the DSN (FR-18.17, MTIX-95.15).
func parseDSN(dsn string) (*url.URL, error) {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return nil, fmt.Errorf("%w: it must start with postgres:// or postgresql://", ErrDSNMalformed)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		// Deliberately not wrapped: err may quote the DSN.
		return nil, fmt.Errorf(
			"%w: percent-encode reserved characters in the user name and password", ErrDSNMalformed)
	}
	return u, nil
}

// resolveHosts returns every host the driver may dial for the DSN u
// with query q: the primary host first, then each fallback, exactly as
// pgconn.ParseConfig resolves them after merging the DSN with PG*
// environment variables and any service file (FR-18.15, MTIX-95.20).
//
// The copy handed to the parser carries sslmode=disable and an empty
// sslrootcert. Neither changes the host list, and together they stop
// the parser from loading certificate and key files, which is the
// pool's job (pgDumpConnParams in cmd/mtix avoids the same eager
// load). u and q are not modified.
//
// The parser's own error quotes the connection string, so it is
// replaced by a fixed message that names no setting value.
func resolveHosts(u *url.URL, q url.Values) ([]*pgconn.FallbackConfig, error) {
	probeQuery := make(url.Values, len(q)+2)
	for k, v := range q {
		probeQuery[k] = append([]string(nil), v...)
	}
	probeQuery.Set("sslmode", "disable")
	probeQuery.Set("sslrootcert", "")
	probe := *u
	probe.RawQuery = probeQuery.Encode()

	cfg, err := pgconn.ParseConfig(probe.String())
	if err != nil {
		// Deliberately not wrapped: err quotes the connection string.
		return nil, fmt.Errorf(
			"connection settings could not be parsed; check the DSN's connection parameters: %w",
			ErrTLSWeakNonLoopback)
	}
	hosts := make([]*pgconn.FallbackConfig, 0, 1+len(cfg.Fallbacks))
	hosts = append(hosts, &pgconn.FallbackConfig{Host: cfg.Host, Port: cfg.Port})
	return append(hosts, cfg.Fallbacks...), nil
}

// isLoopback reports whether host is a loopback host: the name
// "localhost" (any case) or an IP address in 127.0.0.0/8 or ::1
// (FR-18.15). No DNS lookup is made. An empty host, any other name
// and a Unix-domain socket path all report false; the caller decides
// separately whether a socket path is local (MTIX-95.20).
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
