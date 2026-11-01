// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordFor writes a serve record for this live process at the server's port.
func recordFor(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	dir := t.TempDir()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, publishServeRecord(dir, os.Getpid(), "127.0.0.1", port, time.Unix(0, 0)))
	return dir
}

func TestProbeServeHealth_AuthStatuses_OnlyMtixAuthRequiredCounts(t *testing.T) {
	mtixAuth := `{"error":{"code":"AUTH_REQUIRED","message":"a valid bearer token is required"}}`
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"mtix 200 ok", http.StatusOK, `{"status":"ok"}`, true},
		{"mtix 401 with AUTH_REQUIRED", http.StatusUnauthorized, mtixAuth, true},
		{"foreign 401 plain", http.StatusUnauthorized, `unauthorized`, false},
		{"foreign 401 other code", http.StatusUnauthorized, `{"error":{"code":"NOPE"}}`, false},
		{"foreign 403 plain", http.StatusForbidden, `forbidden`, false},
		{"403 even with the code", http.StatusForbidden, mtixAuth, false},
		{"200 wrong status field", http.StatusOK, `{"status":"down"}`, false},
		{"500", http.StatusInternalServerError, mtixAuth, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			_, ok := readServeRecord(recordFor(t, srv))
			assert.Equal(t, tt.want, ok)
		})
	}
}

func TestProbeServeHealth_UnresponsiveServer_TimesOutQuickly(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	dir := recordFor(t, srv)

	done := make(chan bool, 1)
	start := time.Now()
	go func() { _, ok := readServeRecord(dir); done <- ok }()
	select {
	case ok := <-done:
		assert.False(t, ok)
		assert.Less(t, time.Since(start), 3*serveHealthTimeout)
	case <-time.After(5 * time.Second):
		t.Fatal("health probe has no short timeout")
	}
}

func TestRemoveServeRecordIfUnchanged_ReplacedRecord_IsKept(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, publishServeRecord(dir, 111, "127.0.0.1", 6849, at))
	stale, ok := loadServeRecord(dir)
	require.True(t, ok)

	// A new serve publishes its record between the stale read and the removal.
	require.NoError(t, publishServeRecord(dir, 222, "127.0.0.1", 6850, at.Add(time.Hour)))
	removeServeRecordIfUnchanged(dir, stale)
	fresh, ok := loadServeRecord(dir)
	require.True(t, ok, "a record replaced after the read must survive")
	assert.Equal(t, 222, fresh.PID)

	removeServeRecordIfUnchanged(dir, fresh)
	_, statErr := os.Stat(filepath.Join(dir, serveRecordFile))
	assert.True(t, os.IsNotExist(statErr), "an unchanged record is removed")
}

func TestRemoveServeRecordIfUnchanged_SamePIDNewStartTime_IsKept(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, publishServeRecord(dir, 111, "127.0.0.1", 6849, at))
	stale, _ := loadServeRecord(dir)
	require.NoError(t, publishServeRecord(dir, 111, "127.0.0.1", 6849, at.Add(time.Minute)))
	removeServeRecordIfUnchanged(dir, stale)
	_, ok := loadServeRecord(dir)
	assert.True(t, ok)
}

func newAnnounceTestCfg(t *testing.T, now time.Time, log *bytes.Buffer) serveAnnounceConfig {
	t.Helper()
	return serveAnnounceConfig{
		host: "127.0.0.1", port: "6849",
		now:    func() time.Time { return now },
		logger: slog.New(slog.NewTextHandler(log, nil)),
	}
}

func TestAnnounceServe_UsesInjectedClockForStartedAt(t *testing.T) {
	initTestApp(t)
	mtixDir, err := findMtixDir()
	require.NoError(t, err)
	var log bytes.Buffer
	fixed := time.Date(2031, 4, 5, 6, 7, 8, 0, time.UTC)

	remove := announceServe(newAnnounceTestCfg(t, fixed, &log))
	rec, ok := loadServeRecord(mtixDir)
	require.True(t, ok)
	assert.Equal(t, os.Getpid(), rec.PID)
	assert.Equal(t, "2031-04-05T06:07:08Z", rec.StartedAt)

	remove()
	_, ok = loadServeRecord(mtixDir)
	assert.False(t, ok, "the returned remover deletes this serve's record")
}

func TestAnnounceServe_OtherLiveServeRecorded_LeavesItAndWarns(t *testing.T) {
	initTestApp(t)
	mtixDir, err := findMtixDir()
	require.NoError(t, err)
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(healthy.Close)
	child := exec.Command("sleep", "60")
	require.NoError(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	port := healthy.Listener.Addr().(*net.TCPAddr).Port
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, publishServeRecord(mtixDir, child.Process.Pid, "127.0.0.1", port, first))

	var log bytes.Buffer
	remove := announceServe(newAnnounceTestCfg(t, first.Add(time.Hour), &log))
	rec, ok := loadServeRecord(mtixDir)
	require.True(t, ok)
	assert.Equal(t, child.Process.Pid, rec.PID, "the first serve's record must not be overwritten")
	assert.Contains(t, log.String(), "another serve is already recorded")

	remove()
	_, ok = loadServeRecord(mtixDir)
	assert.True(t, ok, "the skipped serve must not remove the other serve's record")
	_ = strconv.Itoa(port)
}

func TestAnnounceServe_PublishFails_WarnsAndDoesNotStopServe(t *testing.T) {
	initTestApp(t)
	mtixDir, err := findMtixDir()
	require.NoError(t, err)
	// A non-empty directory squats on the record path, so the rename fails.
	blocker := filepath.Join(mtixDir, serveRecordFile)
	require.NoError(t, os.MkdirAll(filepath.Join(blocker, "x"), 0o755))

	var log bytes.Buffer
	remove := announceServe(newAnnounceTestCfg(t, time.Unix(0, 0), &log))
	require.NotNil(t, remove)
	assert.Contains(t, log.String(), "serve record not published")
	remove()
	_, statErr := os.Stat(blocker)
	assert.NoError(t, statErr, "nothing was published, so nothing is removed")
}

func TestAnnounceServe_BadPort_WarnsAndPublishesNothing(t *testing.T) {
	initTestApp(t)
	mtixDir, err := findMtixDir()
	require.NoError(t, err)
	var log bytes.Buffer
	cfg := newAnnounceTestCfg(t, time.Unix(0, 0), &log)
	cfg.port = "not-a-port"
	announceServe(cfg)()
	_, ok := loadServeRecord(mtixDir)
	assert.False(t, ok)
	assert.Contains(t, log.String(), "bad port")
}
