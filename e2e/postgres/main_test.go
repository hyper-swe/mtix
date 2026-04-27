// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package postgres

import (
	"flag"
	"fmt"
	"io"
	"os"
	"testing"
)

// providerFlag selects the active provider for the entire test binary.
// The flag is parsed in TestMain so that subtests can read activeProvider()
// without re-parsing.
var providerFlag = flag.String("provider", "",
	"postgres provider: docker | supabase | neon (defaults to MTIX_TEST_PROVIDER or 'docker')")

// activeProviderName returns the provider chosen via -provider, falling back
// to MTIX_TEST_PROVIDER, then "docker".
func activeProviderName() string {
	if providerFlag != nil && *providerFlag != "" {
		return *providerFlag
	}
	if env := os.Getenv(EnvProvider); env != "" {
		return env
	}
	return ProviderDocker
}

// activeProvider builds the provider for the current run. Tests call this
// at the start of every test function; on a missing-credential or
// missing-binary error the test is skipped via t.Skipf so the suite stays
// green for contributors who only have one provider available.
func activeProvider(t *testing.T) PostgresProvider {
	t.Helper()
	p, err := SelectProvider(activeProviderName())
	if err != nil {
		t.Skipf("provider %q unavailable: %s", activeProviderName(), RedactDSN(err.Error()))
	}
	return p
}

// TestMain installs the DSN-redacting writer on os.Stderr (so any stray
// DSN that escapes from a provider implementation is caught at the seam)
// and runs the suite.
func TestMain(m *testing.M) {
	flag.Parse()

	// Wrap stderr so test output never leaks a DSN. Stdout is left untouched
	// (the testing package writes test reports there in a structured way
	// that does not include DSN values).
	original := os.Stderr
	os.Stderr = redactingFile(original)
	defer func() { os.Stderr = original }()

	os.Exit(m.Run())
}

// redactingFile creates a *os.File that redacts DSNs on write. We need a
// real *os.File because os.Stderr is typed as such; we satisfy it by
// piping through a goroutine that drains a pipe-reader into the original
// stderr via the redactor.
func redactingFile(orig *os.File) *os.File {
	r, w, err := os.Pipe()
	if err != nil {
		// Pipe creation should never fail outside of resource exhaustion;
		// if it does, fall back to the unredacted writer rather than block
		// the test binary entirely.
		fmt.Fprintln(orig, "secret_redactor: failed to create pipe; DSN redaction inactive")
		return orig
	}
	rw := NewRedactingWriter(orig)
	go func() {
		_, _ = io.Copy(rw, r)
	}()
	return w
}
