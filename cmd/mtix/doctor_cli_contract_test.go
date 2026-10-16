// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Doctor CLI contracts exercise stock main with owned boards and synthetic configuration.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type doctorCLIOutput struct {
	stdout string
	stderr string
	exit   int
}

func doctorCLIBoard(t *testing.T, relay string) string {
	t.Helper()
	testutil.OperatorState(t)
	t.Setenv("MTIX_SYNC_DSN", "")
	root := t.TempDir()
	dir := filepath.Join(root, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o700))
	config := "prefix: TEST\nsync:\n  auto_sync: false\n"
	if relay != "" {
		config += "  relay.dir: " + strconv.Quote(relay) + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o600))
	store, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return root
}

func doctorCLIEnv(args string, dsn string) []string {
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		// Preserve GOCOVERDIR so instrumented child main writes coverage without warnings.
		if strings.HasPrefix(key, "MTIX_") && key != isolatedOperatorEnv {
			continue
		}
		env = append(env, value)
	}
	return append(env, "MTIX_STDIO_BEHAVIOR_CHILD=1", "MTIX_STDIO_BEHAVIOR_ARGS="+args,
		"MTIX_AUTHOR_ID=doctor-fixture", "MTIX_SYNC_DSN="+dsn)
}

func doctorCLIExecute(t *testing.T, root string, dsn string, args ...string) doctorCLIOutput {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	encoded, err := json.Marshal(append([]string{"-C", root}, args...))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestMCPStdio_ChildMain$")
	cmd.Dir, cmd.Env, cmd.WaitDelay = root, doctorCLIEnv(string(encoded), dsn), 2*time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Start())
	t.Logf("owned stock-main pid=%d command=%s", cmd.Process.Pid, args[0])
	err = cmd.Wait()
	require.NoError(t, ctx.Err(), "owned child exceeded its deadline")
	var exitErr *exec.ExitError
	if err != nil {
		require.True(t, errors.As(err, &exitErr), "child I/O or join failure: %v", err)
	}
	t.Logf("joined stock-main pid=%d exit=%d", cmd.Process.Pid, cmd.ProcessState.ExitCode())
	return doctorCLIOutput{stdout: stdout.String(), stderr: stderr.String(), exit: cmd.ProcessState.ExitCode()}
}

func doctorCLIRows(t *testing.T, body string) []map[string]string {
	t.Helper()
	var rows []map[string]string
	require.NoError(t, json.Unmarshal([]byte(body), &rows), "doctor must emit an array: %s", body)
	require.NotNil(t, rows, "empty output is [] rather than null")
	for _, row := range rows {
		assert.Len(t, row, 4)
		for _, key := range []string{"id", "status", "detail", "fix"} {
			assert.Contains(t, row, key)
		}
		assert.Contains(t, []string{"pass", "warn", "fail", "info"}, row["status"])
	}
	return rows
}

func TestDoctorCLI_Help_DocumentsJSONFixAndExitCodes(t *testing.T) {
	root := doctorCLIBoard(t, "")
	out := doctorCLIExecute(t, root, "", "doctor", "--help")
	assert.Equal(t, 0, out.exit, out.stderr)
	assert.Contains(t, out.stdout, "--fix")
	assert.Contains(t, out.stdout, "--json")
	assert.Contains(t, out.stdout, "non-grant")
	assert.Contains(t, out.stdout, "2")
	assert.Empty(t, out.stderr)
}

func TestDoctorCLI_Unconfigured_DocumentCheckExitZero(t *testing.T) {
	for _, flags := range [][]string{{"--json"}, {"--json", "--fix"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			out := doctorCLIExecute(t, root, "", append([]string{"doctor"}, flags...)...)
			assert.Equal(t, 0, out.exit, out.stderr)
			rows := doctorCLIRows(t, out.stdout)
			wantRows := 1
			if runtime.GOOS == "windows" {
				wantRows++
				assert.Equal(t, "platform.windows", rows[0]["id"])
				assert.Equal(t, "info", rows[0]["status"])
			}
			require.Len(t, rows, wantRows)
			assert.Equal(t, "docs.stale", rows[len(rows)-1]["id"])
			assert.Equal(t, "warn", rows[len(rows)-1]["status"])

			assert.Empty(t, out.stderr)
		})
	}
}

func TestDoctorCLI_BrokenRelay_OnlyApplicableRowsExitTwo(t *testing.T) {
	root := doctorCLIBoard(t, filepath.Join(t.TempDir(), "absent-relay"))
	out := doctorCLIExecute(t, root, "", "doctor", "--json")
	assert.Equal(t, 2, out.exit, out.stderr)
	rows := doctorCLIRows(t, out.stdout)
	var relayFailure bool
	for _, row := range rows {
		if runtime.GOOS == "windows" && row["id"] == "platform.windows" {
			assert.Equal(t, "info", row["status"])
			assert.Empty(t, row["fix"])
			continue
		}
		if row["id"] == "docs.stale" {
			assert.Equal(t, "warn", row["status"])
			continue
		}
		assert.True(t, strings.HasPrefix(row["id"], "sync."), row)
		assert.NotContains(t, row["id"], "pg")
		assert.NotContains(t, row["id"], "schema-current")
		assert.NotContains(t, row["id"], "hub-privileges")
		assert.NotContains(t, row["id"], "hub-triggers")
		if strings.Contains(row["id"], "relay") && row["status"] == "fail" {
			relayFailure = true
			assert.Contains(t, row["detail"]+row["fix"], "sync.relay.dir")
		}
	}
	assert.True(t, relayFailure, "configured unreachable relay must be diagnosed")
	assert.NotContains(t, out.stdout, "PG unreachable")
	assert.Empty(t, out.stderr, "failed checks are already reported on stdout")
}

func TestDoctorCLI_SyntheticDSN_NeverPrintedInTextOrJSON(t *testing.T) {
	const dsn = "postgres://fixture:doctor_secret_2718@/%zz"
	for _, flags := range [][]string{nil, {"--json"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			root := doctorCLIBoard(t, filepath.Join(t.TempDir(), dsn))
			out := doctorCLIExecute(t, root, dsn, append([]string{"doctor"}, flags...)...)
			assert.Equal(t, 2, out.exit, out.stderr)
			assert.NotContains(t, out.stdout+out.stderr, dsn)
			assert.NotContains(t, out.stdout+out.stderr, "doctor_secret_2718")
			assert.Contains(t, out.stdout, "REDACTED", "final relay additions must also be scrubbed")
			if len(flags) != 0 {
				assert.NotEmpty(t, doctorCLIRows(t, out.stdout))
			}
		})
	}
}

func TestDoctorCLI_PositionalDSN_RefusedWithoutEcho(t *testing.T) {
	const dsn = "postgres://fixture:doctor_secret_3141@/%zz"
	root := doctorCLIBoard(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "secrets"), []byte(dsn), 0o600))
	out := doctorCLIExecute(t, root, "", "doctor", dsn)
	assert.Equal(t, 1, out.exit)
	assert.Empty(t, out.stdout)
	assert.NotContains(t, out.stderr, dsn)
	assert.NotContains(t, out.stderr, "doctor_secret_3141")
	assert.Contains(t, out.stderr, "MTIX_SYNC_DSN")
	assert.Contains(t, out.stderr, ".mtix/secrets")
}

func TestDoctorCLI_LegacySync_UnconfiguredShapeOrderAndExitPreserved(t *testing.T) {
	root := doctorCLIBoard(t, "")
	out := doctorCLIExecute(t, root, "", "sync", "doctor", "--json")
	require.Equal(t, 2, out.exit, out.stderr)
	var report map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &report))
	require.Len(t, report, 2)
	require.JSONEq(t, "false", string(report["pass"]))
	var rows []DoctorCheck
	require.NoError(t, json.Unmarshal(report["checks"], &rows))
	require.GreaterOrEqual(t, len(rows), 4)
	assert.Equal(t, "PG reachable", rows[0].Name)
	assert.False(t, rows[0].Pass)
	assert.Contains(t, rows[0].Detail, "DSN")
	assert.Equal(t, "schema current", rows[1].Name)
	assert.False(t, rows[1].Pass)
	assert.Equal(t, "relay configured", rows[len(rows)-1].Name)
	assert.True(t, rows[len(rows)-1].Pass)
	assert.NotContains(t, out.stdout, `"status"`)
	assert.Empty(t, out.stderr)
}
