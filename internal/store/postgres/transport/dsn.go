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
	ErrTLSWeakNonLoopback = errors.New("weak TLS only allowed on loopback hosts")

	// ErrTLSWeakWithoutFlag is returned when the parsed DSN has a weak
	// sslmode but --insecure-tls was not set.
	ErrTLSWeakWithoutFlag = errors.New("weak sslmode requires --insecure-tls")
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

	// Step 3: secrets file.
	secretsPath := filepath.Join(mtixDir, SecretsFilename)
	info, err := os.Stat(secretsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrDSNNotConfigured
		}
		return "", fmt.Errorf("stat %s: %w", secretsPath, err)
	}
	mode := info.Mode().Perm()
	if mode != SecretsRequiredMode {
		return "", fmt.Errorf("%s: %w (want 0600, got %#o)",
			secretsPath, ErrSecretsFileMode, mode)
	}
	body, err := os.ReadFile(secretsPath) //nolint:gosec // path is constructed from caller-supplied mtixDir
	if err != nil {
		return "", fmt.Errorf("read %s: %w", secretsPath, err)
	}
	dsn := strings.TrimSpace(string(body))
	if dsn == "" {
		return "", ErrDSNNotConfigured
	}
	return dsn, nil
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
// A refusal names the host by its position in the resolved list, never
// by its text.
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
			return "", fmt.Errorf("sslmode=%s on host %q: %w", mode, parsed.Hostname(), ErrTLSWeakWithoutFlag)
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

// parseDSN parses a postgres:// or postgresql:// URL form. Falls back
// to wrapping a key=value form into URL form so url.Parse can handle
// it; rejects anything else.
func parseDSN(dsn string) (*url.URL, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return url.Parse(dsn)
	}
	return nil, fmt.Errorf("DSN must start with postgres:// or postgresql://: got prefix %q", dsnPrefix(dsn))
}

// dsnPrefix returns at most the first 16 chars of the DSN for safe
// inclusion in error messages — never the credentials.
func dsnPrefix(dsn string) string {
	const limit = 16
	if len(dsn) <= limit {
		return dsn
	}
	return dsn[:limit] + "..."
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
// replaced by a fixed message that names only the categories of
// setting that can fail to parse, never a value.
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
			"connection settings could not be parsed (check port, connect_timeout, target_session_attrs, service): %w",
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
