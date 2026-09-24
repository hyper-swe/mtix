// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sslModeVerifyFull is the default sslmode and the only one accepted for
// a host that is not loopback or a local socket (FR-18.15).
const sslModeVerifyFull = "verify-full"

// ErrTLSUnverified is returned when, under verify-full, a network host
// of the parsed configuration would not verify the server certificate
// against its own name (FR-18.15, MTIX-95.25).
var ErrTLSUnverified = errors.New("under verify-full every network host must verify the server certificate")

// Approval is the connection configuration the TLS posture check
// approved (FR-18.15, MTIX-95.25). ApproveDSN parses the DSN exactly
// once into Config and evaluates the posture rule on that value;
// NewWithDefaults opens the pool from the same value, so the settings
// the check evaluated are the settings the pool uses.
//
// Config holds the credentials, so never log it. Printing an Approval
// itself, with any fmt verb, shows only the value-free summary of
// String.
type Approval struct {
	// Config is the parsed pool configuration the check evaluated. Every
	// host entry (ConnConfig.Host and each of ConnConfig.Fallbacks)
	// passed the posture rule.
	Config *pgxpool.Config

	// SSLMode is the DSN's sslmode in lower case, verify-full when the
	// DSN names none.
	SSLMode string

	// CASupplied reports whether a TLS host entry of Config carries a
	// root CA pool, whatever its source: sslrootcert in the DSN,
	// MTIX_SYNC_SSLROOTCERT, PGSSLROOTCERT, a service file, or the
	// driver's default ~/.postgresql/root.crt, which it loads when that
	// file exists and no other source names a CA file.
	CASupplied bool

	// dsn is the normalized DSN Config was parsed from: sslmode
	// populated and MTIX_SYNC_SSLROOTCERT honored. It holds the
	// credentials and stays unexported; only EnforceTLSPosture reads it.
	dsn string
}

// ApproveDSN applies the TLS posture to dsn and returns the approved
// configuration (FR-18.15, FR-18.17, MTIX-95.20, MTIX-95.25).
//
// It defaults sslmode to verify-full when the DSN names none, refuses a
// weaker sslmode unless opts.InsecureTLS is set, and adds
// MTIX_SYNC_SSLROOTCERT as sslrootcert when the DSN names none. It then
// parses the normalized DSN with the driver's own parser, the package's
// only connection-settings parse, which merges PG* environment
// variables and any service file and loads the certificate files the
// settings name. The posture rule runs on that parsed configuration:
// under verify-full every network host entry verifies the server
// certificate against its host name; under a weaker sslmode every host
// entry is loopback or a local Unix-domain socket.
//
// Every refusal and parse failure is value-free: a refused host is named
// by its position, and the driver's own error, which quotes the
// connection string, is replaced by one fixed message wrapping only
// ErrDSNMalformed.
func ApproveDSN(dsn string, opts Options) (*Approval, error) {
	u, err := parseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	q := u.Query()
	mode := strings.ToLower(q.Get("sslmode"))
	if mode == "" {
		mode = sslModeVerifyFull
		q.Set("sslmode", mode)
	}
	if mode != sslModeVerifyFull && !opts.InsecureTLS {
		return nil, fmt.Errorf("sslmode=%s: %w", sslModeLabel(mode), ErrTLSWeakWithoutFlag)
	}
	if rootCert := os.Getenv(EnvSSLRootCert); rootCert != "" && q.Get("sslrootcert") == "" {
		q.Set("sslrootcert", rootCert)
	}
	u.RawQuery = q.Encode()
	normalized := u.String()

	cfg, err := pgxpool.ParseConfig(normalized)
	if err != nil {
		// Deliberately not wrapped: err quotes the connection string.
		return nil, unparsableSettings()
	}
	return approveParsed(cfg, mode, normalized)
}

// approveParsed applies the posture rule for mode to cfg, the parsed
// form of the normalized DSN, and returns the Approval that carries cfg
// itself (FR-18.15, MTIX-95.25). Under verify-full every network host
// entry must verify the server certificate against its host name; under
// a weaker sslmode every entry must be loopback or a local socket.
func approveParsed(cfg *pgxpool.Config, mode, normalized string) (*Approval, error) {
	entries := hostEntries(&cfg.ConnConfig.Config)
	if err := checkHostEntries(mode, entries); err != nil {
		return nil, err
	}
	return &Approval{Config: cfg, SSLMode: mode, CASupplied: caSupplied(entries), dsn: normalized}, nil
}

// String returns a summary of a that names no host, path or credential,
// so printing an Approval never repeats the DSN (FR-18.17, MTIX-95.25).
func (a Approval) String() string {
	hosts := 0
	if a.Config != nil {
		_, hosts = hostPositions(hostEntries(&a.Config.ConnConfig.Config))
	}
	return fmt.Sprintf("transport.Approval{sslmode=%s hosts=%d ca_supplied=%t}",
		sslModeLabel(a.SSLMode), hosts, a.CASupplied)
}

// GoString returns String, so the %#v verb is value-free as well
// (MTIX-95.25).
func (a Approval) GoString() string { return a.String() }

// Format writes the String summary for every fmt verb and flag, so no
// verb (%d, %t, %x and the rest) prints the fields of an Approval
// (FR-18.17, MTIX-95.25).
func (a Approval) Format(f fmt.State, _ rune) {
	// Deliberately ignored: fmt.Formatter has no way to report a write
	// error, and fmt records a failed write in its own output state.
	_, _ = io.WriteString(f, a.String())
}

// unparsableSettings returns the one fixed error for connection settings
// the driver cannot parse, a certificate or key file it cannot load
// included, under every sslmode (FR-18.17, MTIX-95.15, MTIX-95.25). It
// wraps only ErrDSNMalformed and names no setting value: no host was
// evaluated, so it is not a host refusal.
func unparsableSettings() error {
	return fmt.Errorf("pgxpool parse: %w: check the DSN's connection parameters", ErrDSNMalformed)
}

// hostEntries lists every connection attempt of cfg in dial order: the
// primary host first, then each fallback (MTIX-95.25). Under allow and
// prefer the driver lists each network host twice, once with TLS and
// once without.
func hostEntries(cfg *pgconn.Config) []*pgconn.FallbackConfig {
	primary := &pgconn.FallbackConfig{Host: cfg.Host, Port: cfg.Port, TLSConfig: cfg.TLSConfig}
	return append([]*pgconn.FallbackConfig{primary}, cfg.Fallbacks...)
}

// checkHostEntries applies the posture rule for mode to entries
// (FR-18.15, MTIX-95.20, MTIX-95.25). Under verify-full every network
// entry must carry TLS that verifies the certificate against the
// entry's host name; a local socket carries no TLS, as the driver never
// wraps one in TLS. Under any other mode every entry must be loopback
// or a local socket. A refusal names the host by its position only.
func checkHostEntries(mode string, entries []*pgconn.FallbackConfig) error {
	positions, count := hostPositions(entries)
	for i, e := range entries {
		network, _ := pgconn.NetworkAddress(e.Host, e.Port)
		local := network == "unix"
		if mode == sslModeVerifyFull {
			if local || verifiesHost(e) {
				continue
			}
			return fmt.Errorf("sslmode=%s: host %d of %d is not verified: %w", mode, positions[i], count, ErrTLSUnverified)
		}
		if !local && !isLoopback(e.Host) {
			return fmt.Errorf("sslmode=%s: host %d of %d is not loopback or a local socket: %w",
				sslModeLabel(mode), positions[i], count, ErrTLSWeakNonLoopback)
		}
	}
	return nil
}

// verifiesHost reports whether e's TLS settings verify the server
// certificate against e's host name (MTIX-95.25).
func verifiesHost(e *pgconn.FallbackConfig) bool {
	return e.TLSConfig != nil && !e.TLSConfig.InsecureSkipVerify && e.TLSConfig.ServerName == e.Host
}

// hostPositions returns the 1-based host position of each entry and the
// number of hosts, counting consecutive entries for the same host and
// port once, so a refusal names a host as the DSN lists it
// (MTIX-95.25).
func hostPositions(entries []*pgconn.FallbackConfig) ([]int, int) {
	positions := make([]int, len(entries))
	count := 0
	for i, e := range entries {
		if i == 0 || e.Host != entries[i-1].Host || e.Port != entries[i-1].Port {
			count++
		}
		positions[i] = count
	}
	return positions, count
}

// caSupplied reports whether any TLS entry carries a root CA pool
// (MTIX-95.25).
func caSupplied(entries []*pgconn.FallbackConfig) bool {
	for _, e := range entries {
		if e.TLSConfig != nil && e.TLSConfig.RootCAs != nil {
			return true
		}
	}
	return false
}

// sslModeLabel returns mode when it is an sslmode the driver knows and
// "(unrecognized)" otherwise, so a message never repeats other DSN text
// (FR-18.17, MTIX-95.25).
func sslModeLabel(mode string) string {
	switch mode {
	case "disable", "allow", "prefer", "require", "verify-ca", sslModeVerifyFull:
		return mode
	default:
		return "(unrecognized)"
	}
}
