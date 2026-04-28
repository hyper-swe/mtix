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
	// but the host is not loopback.
	ErrTLSWeakNonLoopback = errors.New("weak TLS only allowed on loopback hosts")

	// ErrTLSWeakWithoutFlag is returned when the parsed DSN has a weak
	// sslmode but --insecure-tls was not set.
	ErrTLSWeakWithoutFlag = errors.New("weak sslmode requires --insecure-tls")
)

// Options control non-DSN behavior of the transport.
type Options struct {
	// InsecureTLS allows sslmode weaker than verify-full ONLY when the
	// host is a loopback address. Default false.
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
// set AND the host is a loopback address.
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
		host := parsed.Hostname()
		if !opts.InsecureTLS {
			return "", fmt.Errorf("sslmode=%s on host %q: %w", mode, host, ErrTLSWeakWithoutFlag)
		}
		if !isLoopback(host) {
			return "", fmt.Errorf("sslmode=%s on host %q: %w", mode, host, ErrTLSWeakNonLoopback)
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

// isLoopback reports whether host resolves to a loopback address.
// Accepts the literal strings "localhost", "127.0.0.1", "::1" without
// DNS resolution; anything else is checked against net.ParseIP.
func isLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
