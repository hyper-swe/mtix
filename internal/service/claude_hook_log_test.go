// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func newHookLog(t *testing.T) *ClaudeHookLog {
	t.Helper()
	return &ClaudeHookLog{Dir: filepath.Join(t.TempDir(), "claude-hooks"), Clock: func() time.Time {
		return time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	}}
}

func TestClaudeHookLog_UnsafeAgentID_RefusedBeforeAnyFileIsWritten(t *testing.T) {
	for _, id := range []string{"", "../x", "a/b", `a\b`, ".hidden", "a b", strings.Repeat("a", 65)} {
		t.Run(id, func(t *testing.T) {
			log := newHookLog(t)
			err := log.Record(id, ClaudeHookStop, errors.New("x"))
			require.ErrorIs(t, err, model.ErrInvalidInput)
			_, statErr := os.Stat(log.Dir)
			assert.True(t, os.IsNotExist(statErr), "nothing is created for a refused id")
		})
	}
}

func TestClaudeHookLog_Record_StoresLatestBoundedPrivateFailure(t *testing.T) {
	log := newHookLog(t)
	require.NoError(t, log.Record("worker", ClaudeHookStop, errors.New("first")))
	require.NoError(t, log.Record("worker", ClaudeHookStop, errors.New(strings.Repeat("x", 1000))))

	failures, err := log.Failures()
	require.NoError(t, err)
	require.Len(t, failures, 1, "one record per agent and event")
	assert.Len(t, failures[0].Error, claudeHookErrorMax)
	assert.Equal(t, time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC), failures[0].At)
	info, err := os.Stat(filepath.Join(log.Dir, "worker.stop.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dir, err := os.Stat(log.Dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm())
}

func TestClaudeHookLog_NilErr_ClearsOnlyThatHook(t *testing.T) {
	log := newHookLog(t)
	require.NoError(t, log.Record("worker", ClaudeHookStop, errors.New("a")))
	require.NoError(t, log.Record("worker", ClaudeHookSessionStart, errors.New("b")))
	require.NoError(t, log.Record("worker", ClaudeHookStop, nil))
	require.NoError(t, log.Record("worker", ClaudeHookStop, nil), "clearing a missing record is not an error")

	failures, err := log.Failures()
	require.NoError(t, err)
	require.Len(t, failures, 1)
	assert.Equal(t, ClaudeHookSessionStart, failures[0].Event)
}

func TestClaudeHookLog_NilLogAndMissingDir_AreEmpty(t *testing.T) {
	var none *ClaudeHookLog
	require.NoError(t, none.Record("worker", ClaudeHookStop, errors.New("x")))
	failures, err := newHookLog(t).Failures()
	require.NoError(t, err)
	assert.Empty(t, failures)
}

func TestClaudeHookLog_CorruptRecord_IsReportedNotSkipped(t *testing.T) {
	log := newHookLog(t)
	require.NoError(t, os.MkdirAll(log.Dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(log.Dir, "worker.stop.json"), []byte("{not json"), 0o600))
	_, err := log.Failures()
	require.Error(t, err)

	check := ClaudeHookDoctorCheck(log)
	res := check.Run(t.Context())
	assert.Equal(t, DoctorFail, res.Status, "an unreadable record must not read as healthy")
	assert.Equal(t, "mtix doctor --json", res.Fix)
}

func TestClaudeHookDoctorCheck_PassesWithoutFailures(t *testing.T) {
	res := ClaudeHookDoctorCheck(newHookLog(t)).Run(t.Context())
	assert.Equal(t, DoctorInfo, res.Status, "every wake.listener row is informational when healthy")
	assert.Equal(t, "mtix doctor --json", res.Fix, "every wake.listener row carries a runnable fix")
}

func TestClaudeHookDoctorCheck_Failure_ExactFixAndJSONData(t *testing.T) {
	log := newHookLog(t)
	require.NoError(t, log.Record("worker", ClaudeHookSessionStart, errors.New("register agent: boom")))
	check := ClaudeHookDoctorCheck(log)
	assert.Equal(t, "wake.listener.hook-errors", check.ID)

	res := check.Run(t.Context())

	assert.Equal(t, DoctorFail, res.Status)
	assert.Equal(t, "mtix hook claude-session session-start --agent worker --timeout 5", res.Fix)
	assert.Contains(t, res.Detail, "register agent: boom")
	assert.Contains(t, res.Detail, "2026-11-01T09:30:00Z")
	data, err := json.Marshal(res)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"failures":[{"agent":"worker","event":"session-start"`)
	assert.NotContains(t, res.Fix, "<", "a fix never carries a placeholder")
}

func TestClaudeHookLog_Record_LongMultiByteText_StaysValidUTF8(t *testing.T) {
	log := newHookLog(t)
	text := "a" + strings.Repeat("é", 200) // byte 300 falls inside a rune
	require.NoError(t, log.Record("worker", ClaudeHookStop, errors.New(text)))

	failures, err := log.Failures()
	require.NoError(t, err)
	require.Len(t, failures, 1)
	assert.True(t, utf8.ValidString(failures[0].Error))
	assert.NotContains(t, failures[0].Error, "�", "no replacement character from a cut rune")
	assert.LessOrEqual(t, len(failures[0].Error), claudeHookErrorMax)
	assert.Equal(t, "a"+strings.Repeat("é", 149), failures[0].Error)
}

func TestClaudeHookLog_Failures_StopsAtTheRecordLimit(t *testing.T) {
	log := newHookLog(t)
	require.NoError(t, os.MkdirAll(log.Dir, 0o700))
	for i := 0; i < claudeHookRecordLimit+44; i++ {
		name := fmt.Sprintf("agent%03d.stop.json", i)
		body := fmt.Sprintf(`{"agent":"agent%03d","event":"stop","error":"x"}`, i)
		require.NoError(t, os.WriteFile(filepath.Join(log.Dir, name), []byte(body), 0o600))
	}

	failures, err := log.Failures()

	require.NoError(t, err)
	assert.Len(t, failures, claudeHookRecordLimit, "a hostile directory cannot flood doctor")
}
