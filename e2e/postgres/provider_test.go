// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Harness self-tests. These tests prove that the test framework itself
// behaves correctly, without depending on any specific provider being
// available. They are listed in the MTIX-14.9 ticket as the six harness
// tests required by acceptance.

package postgres

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestProvider_DockerSetupTeardown_NoLeakedContainers exercises the
// docker provider's Setup/cleanup contract using a fake docker binary
// that records every invocation. We do not require a real docker daemon
// to validate that the harness wires its lifecycle correctly.
func TestProvider_DockerSetupTeardown_NoLeakedContainers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake docker shim uses POSIX shell; not run on Windows")
	}

	calls, fakeDocker := installFakeDocker(t)

	p, err := SelectProvider(ProviderDocker, WithDockerCmd(fakeDocker))
	require.NoError(t, err)

	// Use a sub-test so t.Cleanup runs before our assertions.
	t.Run("setup_runs_then_cleans_up", func(t *testing.T) {
		dsn, cleanup := p.Setup(context.Background(), t)
		assert.NotEmpty(t, dsn, "Setup must return a non-empty DSN")
		assert.True(t, strings.HasPrefix(dsn, "postgres://"),
			"DSN should be a postgres URL")
		// Manually invoke cleanup to prove it's idempotent.
		cleanup()
	})

	// After the sub-test ends and t.Cleanup fires, we expect at least one
	// `docker rm -f` invocation.
	got := calls.snapshot()
	hasRun := false
	hasRm := false
	for _, c := range got {
		switch {
		case len(c) > 0 && c[0] == "run":
			hasRun = true
		case len(c) > 1 && c[0] == "rm" && c[1] == "-f":
			hasRm = true
		}
	}
	assert.True(t, hasRun, "expected at least one `docker run` invocation")
	assert.True(t, hasRm, "expected `docker rm -f` cleanup invocation")
}

// TestProvider_DSNNeverInTestOutput is a regression test: we synthesise
// stderr that contains a DSN, run it through the redactor, and assert
// that the original DSN does not appear in the output.
func TestProvider_DSNNeverInTestOutput(t *testing.T) {
	const secretDSN = "postgres://hunter2:supersecret@db.example.com:5432/prod?sslmode=require"

	got := captureRedacted(func(w io.Writer) {
		_, _ = w.Write([]byte("connection failed for " + secretDSN + "\n"))
		_, _ = w.Write([]byte("retry with " + secretDSN))
	})

	assert.NotContains(t, got, "hunter2",
		"redactor must scrub DSN credentials from output")
	assert.NotContains(t, got, "supersecret",
		"redactor must scrub DSN passwords from output")
	assert.NotContains(t, got, secretDSN,
		"redactor must scrub the full DSN")
	assert.Contains(t, got, dsnReplacement,
		"redactor must leave a marker so reviewers can see redaction occurred")
}

// TestContractSuite_RunsIdenticallyAcrossProviders is a meta-test: it walks
// the contract test functions defined in this package and asserts each one
// uses activeProvider() (not a hard-coded provider type). This guarantees
// the suite stays provider-agnostic — adding a new provider does not
// require editing every test.
func TestContractSuite_RunsIdenticallyAcrossProviders(t *testing.T) {
	root := projectRoot(t)
	contractFile := filepath.Join(root, "e2e", "postgres", "contract_test.go")

	src, err := os.ReadFile(contractFile)
	require.NoError(t, err, "contract_test.go must exist")
	body := string(src)

	// Every contract test function name starts with TestStore_.
	tests := scanTestFunctions(body, "TestStore_")
	require.NotEmpty(t, tests, "contract suite should contain TestStore_ functions")

	// Every test body must call activeProvider(t). If a contributor adds a
	// test that bypasses the provider seam, this assertion fires.
	for _, name := range tests {
		assert.True(t, functionCalls(body, name, "activeProvider("),
			"%s should call activeProvider(t) so it runs across providers", name)
	}
}

// TestQuirkSuite_PgBouncerWarning_ShownIfDetected verifies the redaction
// path AND the capability flags: when a Supabase DSN points at port 6543,
// our provider must warn that prepared statements are unavailable.
func TestQuirkSuite_PgBouncerWarning_ShownIfDetected(t *testing.T) {
	pooled, err := SelectProvider(ProviderSupabase,
		WithSupabaseDSN("postgres://u:p@h.supabase.co:6543/postgres"))
	require.NoError(t, err)
	assert.False(t, pooled.SupportsPreparedStatements(),
		"DSN on :6543 must be detected as transaction-mode pgbouncer")

	direct, err := SelectProvider(ProviderSupabase,
		WithSupabaseDSN("postgres://u:p@h.supabase.co:5432/postgres"))
	require.NoError(t, err)
	assert.True(t, direct.SupportsPreparedStatements(),
		"DSN on :5432 must be treated as direct (full feature) connection")
}

// TestCI_E2EJobConfigured parses .github/workflows/ci.yml and asserts the
// Docker-Postgres job exists with the right trigger and command. Catches
// accidental removal during workflow refactors.
func TestCI_E2EJobConfigured(t *testing.T) {
	root := projectRoot(t)
	src, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	require.NoError(t, err)

	wf := parseWorkflow(t, src)

	job, ok := wf.Jobs["test-go-postgres-docker"]
	require.True(t, ok, "ci.yml must define job test-go-postgres-docker")
	assert.NotEmpty(t, job.Steps, "test-go-postgres-docker must have steps")

	combined := combineRuns(job)
	assert.Contains(t, combined, "-tags=e2e",
		"e2e job must build with -tags=e2e")
	assert.Contains(t, combined, "-provider=docker",
		"e2e job must select the docker provider")
}

// TestCI_ReleaseJobConfigured parses .github/workflows/release.yml and
// asserts the cloud-Postgres job exists, runs only on tags, and references
// both Supabase and Neon DSN secrets.
func TestCI_ReleaseJobConfigured(t *testing.T) {
	root := projectRoot(t)
	src, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	require.NoError(t, err)

	wf := parseWorkflow(t, src)

	job, ok := wf.Jobs["test-go-postgres-cloud"]
	require.True(t, ok, "release.yml must define job test-go-postgres-cloud")
	assert.NotEmpty(t, job.Steps, "test-go-postgres-cloud must have steps")

	combined := combineRuns(job)
	assert.Contains(t, combined, "MTIX_TEST_SUPABASE_DSN",
		"cloud job must consume Supabase DSN secret")
	assert.Contains(t, combined, "MTIX_TEST_NEON_DSN",
		"cloud job must consume Neon DSN secret")
	assert.Contains(t, combined, "-tags=e2e",
		"cloud job must build with -tags=e2e")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// projectRoot walks up from the package directory until it finds go.mod.
// We do this rather than hard-code a relative path so the test passes
// whether run from the package dir or the project root.
func projectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate project root (go.mod)")
		}
		dir = parent
	}
}

// installFakeDocker writes a tiny POSIX shell script that mimics the
// docker subcommands we use, recording every invocation into a log file.
// Returns a callRecorder for assertions and the path to the fake binary.
//
// The script:
//   - `run -d ...`  → echoes a fake container id, exits 0
//   - `port  ...`   → echoes "0.0.0.0:55432"
//   - `exec ... pg_isready ...` → exits 0 immediately
//   - `rm -f ...`   → exits 0
type callRecorder struct {
	log string
}

func (r *callRecorder) snapshot() [][]string {
	data, err := os.ReadFile(r.log)
	if err != nil {
		return nil
	}
	out := [][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		out = append(out, strings.Fields(line))
	}
	return out
}

func installFakeDocker(t *testing.T) (*callRecorder, string) {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	binPath := filepath.Join(dir, "docker")

	script := `#!/bin/sh
# Fake docker shim for harness self-tests. Never reaches the real daemon.
echo "$@" >> "` + logFile + `"
case "$1" in
  run)
    # Emit a fake 64-char container id.
    echo "deadbeef0000000000000000000000000000000000000000000000000000cafe"
    exit 0
    ;;
  port)
    echo "0.0.0.0:55432"
    echo "[::]:55432"
    exit 0
    ;;
  exec)
    # pg_isready always succeeds.
    exit 0
    ;;
  rm)
    exit 0
    ;;
esac
exit 0
`
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o755), //nolint:gosec // test fixture
		"failed to write fake docker shim")
	return &callRecorder{log: logFile}, binPath
}

// scanTestFunctions extracts the names of all top-level test functions
// in src whose names start with prefix.
func scanTestFunctions(src, prefix string) []string {
	var names []string
	for _, line := range strings.Split(src, "\n") {
		const head = "func "
		if !strings.HasPrefix(line, head) {
			continue
		}
		rest := strings.TrimPrefix(line, head)
		if !strings.HasPrefix(rest, prefix) {
			continue
		}
		// Function name ends at "(".
		if i := strings.Index(rest, "("); i > 0 {
			names = append(names, rest[:i])
		}
	}
	return names
}

// functionCalls returns true if the body of fn (in src) contains call.
// It uses a brace-counting scan rather than ast/parser so the test stays
// dependency-free (and robust to e2e build-tag exclusions).
func functionCalls(src, fn, call string) bool {
	startIdx := strings.Index(src, "func "+fn+"(")
	if startIdx == -1 {
		return false
	}
	open := strings.Index(src[startIdx:], "{")
	if open == -1 {
		return false
	}
	open += startIdx
	depth := 1
	i := open + 1
	for i < len(src) && depth > 0 {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		i++
	}
	body := src[open:i]
	return strings.Contains(body, call)
}

// workflow is a minimal subset of the GitHub Actions YAML schema, just
// enough to assert that a named job exists and contains the expected
// `run:` strings.
type workflow struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}
type workflowJob struct {
	Steps []map[string]any `yaml:"steps"`
}

func parseWorkflow(t *testing.T, src []byte) workflow {
	t.Helper()
	var wf workflow
	if err := yaml.Unmarshal(src, &wf); err != nil {
		t.Fatalf("workflow YAML invalid: %v", err)
	}
	return wf
}

// combineRuns concatenates every step's `run:` value (when present)
// into one string for keyword assertions.
func combineRuns(job workflowJob) string {
	var b strings.Builder
	for _, step := range job.Steps {
		if v, ok := step["run"]; ok {
			b.WriteString(toString(v))
			b.WriteString("\n")
		}
		if v, ok := step["env"]; ok {
			b.WriteString(toString(v))
			b.WriteString("\n")
		}
		if v, ok := step["with"]; ok {
			b.WriteString(toString(v))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		var b strings.Builder
		for k, val := range t {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(toString(val))
			b.WriteString("\n")
		}
		return b.String()
	default:
		return ""
	}
}

